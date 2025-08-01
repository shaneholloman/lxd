package rsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/canonical/lxd/lxd/linux"
	"github.com/canonical/lxd/shared"
	"github.com/canonical/lxd/shared/ioprogress"
	"github.com/canonical/lxd/shared/logger"
	"github.com/canonical/lxd/shared/version"
)

// Debug controls additional debugging in rsync output.
var Debug bool

// RunWrapper is an optional function that's used to wrap rsync, useful for confinement like AppArmor.
// It returns a cleanup function that will close the wrapper's environment, and should be called after the command has completed.
var RunWrapper func(cmd *exec.Cmd, source string, destination string) (func(), error)

// rsync is a wrapper for the rsync command which will respect RunWrapper.
func rsync(args ...string) (string, error) {
	if len(args) < 2 {
		return "", errors.New("rsync call expects a minimum of two arguments (source and destination)")
	}

	// Setup the command.
	cmd := exec.Command("rsync", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	// Call the wrapper if defined.
	if RunWrapper != nil {
		source := args[len(args)-2]
		destination := args[len(args)-1]

		cleanup, err := RunWrapper(cmd, source, destination)
		if err != nil {
			return "", err
		}

		defer cleanup()
	}

	// Run the command.
	err := cmd.Run()
	if err != nil {
		return stdout.String(), shared.NewRunError("rsync", args, err, &stdout, &stderr)
	}

	return stdout.String(), nil
}

func runRsync(source string, dest string, bwlimit string, xattrs bool, rsyncArgs ...string) (string, error) {
	err := os.MkdirAll(dest, 0755)
	if err != nil {
		return "", err
	}

	rsyncVerbosity := "-q"
	if Debug {
		rsyncVerbosity = "-vi"
	}

	args := []string{
		"-a",
		"-HA",
		"--sparse",
		"--devices",
		"--delete",
		"--numeric-ids",
		// Checks for file modifications on nanoseconds granularity.
		"--modify-window=-1",
	}

	if xattrs {
		args = append(args, "--xattrs")
		if AtLeast("3.1.3") {
			args = append(args, "--filter=-x security.selinux")
		}
	}

	if bwlimit != "" {
		args = append(args, "--bwlimit", bwlimit)
	}

	if len(rsyncArgs) > 0 {
		args = append(args, rsyncArgs...)
	}

	args = append(args,
		rsyncVerbosity,
		source,
		dest)

	msg, err := rsync(args...)
	if err != nil {
		runError, ok := err.(shared.RunError)
		if ok {
			exitError, ok := runError.Unwrap().(*exec.ExitError)
			if ok {
				if exitError.ExitCode() == 24 {
					return msg, nil
				}
			}
		}

		return msg, err
	}

	return msg, nil
}

// LocalCopy copies a directory using rsync (with the --devices option).
func LocalCopy(source string, dest string, bwlimit string, xattrs bool, rsyncArgs ...string) (string, error) {
	return runRsync(shared.AddSlash(source), dest, bwlimit, xattrs, rsyncArgs...)
}

// CopyFile copies a single file using rsync (with the --devices option).
func CopyFile(source string, dest string, bwlimit string, xattrs bool, rsyncArgs ...string) (string, error) {
	return runRsync(strings.TrimSuffix(source, "/"), dest, bwlimit, xattrs, rsyncArgs...)
}

// Send sets up the sending half of an rsync, to recursively send the
// directory pointed to by path over the websocket.
func Send(name string, path string, conn io.ReadWriteCloser, tracker *ioprogress.ProgressTracker, features []string, bwlimit string, execPath string, rsyncArgs ...string) error {
	/*
	 * The way rsync works, it invokes a subprocess that does the actual
	 * talking (given to it by a -E argument). Since there isn't an easy
	 * way for us to capture this process' stdin/stdout, we just use netcat
	 * and write to/from a unix socket.
	 *
	 * In principle we don't need this socket. It seems to me that some
	 * clever invocation of rsync --server --sender and usage of that
	 * process' stdin/stdout could work around the need for this socket,
	 * but I couldn't get it to work. Another option would be to look at
	 * the spawned process' first child and read/write from its
	 * stdin/stdout, but that also seemed messy. In any case, this seems to
	 * work just fine.
	 */
	auds := "@lxd/" + uuid.New().String()
	// We simply copy a part of the uuid if it's longer than the allowed
	// maximum. That should be safe enough for our purposes.
	if len(auds) > linux.ABSTRACT_UNIX_SOCK_LEN-1 {
		auds = auds[:linux.ABSTRACT_UNIX_SOCK_LEN-1]
	}

	l, err := net.Listen("unix", auds)
	if err != nil {
		return err
	}

	defer func() { _ = l.Close() }()

	/*
	 * Here, the path /tmp/foo is ignored. Since we specify localhost,
	 * rsync thinks we are syncing to a remote host (in this case, the
	 * other end of the lxd websocket), and so the path specified on the
	 * --server instance of rsync takes precedence.
	 */
	rsyncCmd := fmt.Sprintf("%s netcat %s %s --", execPath, auds, name)

	args := []string{
		"-ar",
		"--devices",
		"--numeric-ids",
		"--partial",
		"--sparse",
	}

	if bwlimit != "" {
		args = append(args, "--bwlimit", bwlimit)
	}

	if len(features) > 0 {
		args = append(args, rsyncFeatureArgs(features)...)
	}

	if len(rsyncArgs) > 0 {
		args = append(args, rsyncArgs...)
	}

	args = append(args, []string{
		path,
		"localhost:/tmp/foo",
		"-e",
		rsyncCmd}...)

	cmd := exec.Command("rsync", args...)

	// Call the wrapper if defined.
	if RunWrapper != nil {
		cleanup, err := RunWrapper(cmd, path, "")
		if err != nil {
			return err
		}

		defer cleanup()
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	err = cmd.Start()
	if err != nil {
		return err
	}

	var ncConn *net.Conn
	chConn := make(chan *net.Conn, 1)

	go func() {
		ncConn, err := l.Accept()
		if err != nil {
			chConn <- nil
			return
		}

		chConn <- &ncConn
	}()

	select {
	case ncConn = <-chConn:
		if ncConn == nil {
			output, _ := io.ReadAll(stderr)
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return fmt.Errorf("Failed to ncConnect to rsync socket (%s)", string(output))
		}

	case <-time.After(10 * time.Second):
		output, _ := io.ReadAll(stderr)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("rsync failed to spawn after 10s (%s)", string(output))
	}

	// Setup progress tracker.
	netcatConn := *ncConn
	readNetcatPipe := io.ReadCloser(netcatConn)
	if tracker != nil {
		readNetcatPipe = &ioprogress.ProgressReader{
			ReadCloser: netcatConn,
			Tracker:    tracker,
		}
	}

	// Forward from netcat to target.
	chCopyNetcat := make(chan error, 1)
	go func() {
		_, err := io.Copy(conn, readNetcatPipe)
		chCopyNetcat <- err
		_ = readNetcatPipe.Close()
		_ = netcatConn.Close()
		_ = conn.Close() // sends barrier message.
	}()

	// Forward from target to netcat.
	writeNetcatPipe := io.WriteCloser(netcatConn)
	chCopyTarget := make(chan error, 1)
	go func() {
		_, err := io.Copy(writeNetcatPipe, conn)
		chCopyTarget <- err
		_ = writeNetcatPipe.Close()
	}()

	// Wait for rsync to complete.
	output, err := io.ReadAll(stderr)
	if err != nil {
		_ = cmd.Process.Kill()
		logger.Errorf("Rsync stderr read failed: %s: %v", path, err)
	}

	err = cmd.Wait()
	errs := []error{}
	chCopyNetcatErr := <-chCopyNetcat
	chCopyTargetErr := <-chCopyTarget

	if err != nil {
		errs = append(errs, err)

		// Try to get more info about the error.
		if chCopyNetcatErr != nil {
			errs = append(errs, chCopyNetcatErr)
		}

		if chCopyTargetErr != nil {
			errs = append(errs, chCopyTargetErr)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("Rsync send failed: %s, %s: %v (%s)", name, path, errs, string(output))
	}

	return nil
}

// Recv sets up the receiving half of the websocket to rsync (the other
// half set up by rsync.Send), putting the contents in the directory specified
// by path.
func Recv(path string, conn io.ReadWriteCloser, tracker *ioprogress.ProgressTracker, features []string) error {
	args := []string{
		"--server",
		"-vlogDtpre.iLsfx",
		"--numeric-ids",
		"--devices",
		"--partial",
		"--sparse",
		// This flag is only required on the receiving end.
		// Checks for file modifications on nanoseconds granularity.
		"--modify-window=-1",
	}

	if len(features) > 0 {
		args = append(args, rsyncFeatureArgs(features)...)
	}

	args = append(args, []string{".", path}...)

	cmd := exec.Command("rsync", args...)

	// Call the wrapper if defined.
	if RunWrapper != nil {
		cleanup, err := RunWrapper(cmd, "", path)
		if err != nil {
			return err
		}

		defer cleanup()
	}

	// Forward from rsync to source.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	chCopyRsync := make(chan error, 1)
	go func() {
		_, err := io.Copy(conn, stdout)
		_ = stdout.Close()
		_ = conn.Close() // sends barrier message.
		chCopyRsync <- err
	}()

	// Forward from source to rsync.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}

	readSourcePipe := io.ReadCloser(conn)
	if tracker != nil {
		readSourcePipe = &ioprogress.ProgressReader{
			ReadCloser: conn,
			Tracker:    tracker,
		}
	}

	chCopySource := make(chan error, 1)
	go func() {
		_, err := io.Copy(stdin, readSourcePipe)
		_ = stdin.Close()
		chCopySource <- err
	}()

	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = cmd.Process.Kill()
		logger.Errorf("Rsync stderr read failed: %s: %v", path, err)
	}

	err = cmd.Start()
	if err != nil {
		return err
	}

	output, err := io.ReadAll(stderr)
	if err != nil {
		logger.Errorf("Rsync stderr read failed: %s: %v", path, err)
	}

	err = cmd.Wait()
	errs := []error{}
	chCopyRsyncErr := <-chCopyRsync
	chCopySourceErr := <-chCopySource

	if err != nil {
		errs = append(errs, err)

		// Try to get more info about the error.
		if chCopyRsyncErr != nil {
			errs = append(errs, chCopyRsyncErr)
		}

		if chCopySourceErr != nil {
			errs = append(errs, chCopySourceErr)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("Rsync receive failed: %s: %v (%s)", path, errs, string(output))
	}

	return nil
}

func rsyncFeatureArgs(features []string) []string {
	args := []string{}
	if slices.Contains(features, "xattrs") {
		args = append(args, "--xattrs")
		if AtLeast("3.1.3") {
			args = append(args, "--filter=-x security.selinux")
		}
	}

	if slices.Contains(features, "delete") {
		args = append(args, "--delete")
	}

	if slices.Contains(features, "compress") {
		args = append(args, "--compress", "--compress-level=2")
	}

	return args
}

// AtLeast compares the local version to a minimum version.
func AtLeast(minimum string) bool {
	// Parse the current version.
	out, err := shared.RunCommandContext(context.TODO(), "rsync", "--version")
	if err != nil {
		return false
	}

	fields := strings.Split(strings.SplitN(out, "\n", 2)[0], "  ")
	if len(fields) < 3 {
		return false
	}

	versionStr := strings.TrimPrefix(fields[1], "version ")

	ver, err := version.Parse(versionStr)
	if err != nil {
		return false
	}

	// Load minium version.
	minVer, err := version.NewDottedVersion(minimum)
	if err != nil {
		return false
	}

	return ver.Compare(minVer) >= 0
}
