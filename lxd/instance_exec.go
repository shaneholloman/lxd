package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"golang.org/x/sys/unix"

	"github.com/canonical/lxd/lxd/cluster"
	"github.com/canonical/lxd/lxd/db/operationtype"
	"github.com/canonical/lxd/lxd/instance"
	"github.com/canonical/lxd/lxd/instance/drivers"
	"github.com/canonical/lxd/lxd/instance/instancetype"
	"github.com/canonical/lxd/lxd/operations"
	"github.com/canonical/lxd/lxd/request"
	"github.com/canonical/lxd/lxd/response"
	"github.com/canonical/lxd/lxd/state"
	"github.com/canonical/lxd/shared"
	"github.com/canonical/lxd/shared/api"
	"github.com/canonical/lxd/shared/cancel"
	"github.com/canonical/lxd/shared/logger"
	"github.com/canonical/lxd/shared/version"
	"github.com/canonical/lxd/shared/ws"
)

const execWSControl = -1
const execWSStdin = 0
const execWSStdout = 1
const execWSStderr = 2

type execWs struct {
	req api.InstanceExecPost

	instance              instance.Instance
	conns                 map[int]*websocket.Conn
	connsLock             sync.Mutex
	waitRequiredConnected cancel.Canceller
	waitControlConnected  cancel.Canceller
	fds                   map[int]string
	s                     *state.State
}

// Metadata returns a map of metadata.
func (s *execWs) Metadata() any {
	fds := shared.Jmap{}
	for fd, secret := range s.fds {
		if fd == execWSControl {
			fds[api.SecretNameControl] = secret
		} else {
			fds[strconv.Itoa(fd)] = secret
		}
	}

	return shared.Jmap{
		"fds":         fds,
		"command":     s.req.Command,
		"environment": s.req.Environment,
		"interactive": s.req.Interactive,
	}
}

// Connect connects to the websocket.
func (s *execWs) Connect(op *operations.Operation, r *http.Request, w http.ResponseWriter) error {
	secret := r.FormValue("secret")
	if secret == "" {
		return errors.New("missing secret")
	}

	err := op.CheckRequestor(r)
	if err != nil {
		return err
	}

	for fd, fdSecret := range s.fds {
		if secret == fdSecret {
			conn, err := ws.Upgrader.Upgrade(w, r, nil)
			if err != nil {
				return err
			}

			s.connsLock.Lock()

			val, found := s.conns[fd]
			if found && val == nil {
				s.conns[fd] = conn

				ws.StartKeepAlive(conn)

				if fd == execWSControl {
					s.waitControlConnected.Cancel() // Control connection connected.
				}

				for i, c := range s.conns {
					if i == execWSControl && s.req.WaitForWS && !s.req.Interactive {
						// Due to a historical bug in the LXC CLI command, we cannot force
						// the client to connect a control socket when in non-interactive
						// mode. This is because the older CLI tools did not connect this
						// channel and so we would prevent the older CLIs connecting to
						// newer servers. So skip the control connection from being
						// considered as a required connection in this case.
						continue
					}

					if c == nil {
						s.connsLock.Unlock()
						return nil // Not all required connections connected yet.
					}
				}

				s.waitRequiredConnected.Cancel() // All required connections now connected.
				s.connsLock.Unlock()
				return nil
			} else if !found {
				return errors.New("Unknown websocket number")
			}

			return errors.New("Websocket number already connected")
		}
	}

	/* If we didn't find the right secret, the user provided a bad one,
	 * which 403, not 404, since this operation actually exists */
	return os.ErrPermission
}

// Do connects to the websocket and executes the operation.
func (s *execWs) Do(op *operations.Operation) error {
	// Once this function ends ensure that any connected websockets are closed.
	defer func() {
		s.connsLock.Lock()
		for i := range s.conns {
			if s.conns[i] != nil {
				_ = s.conns[i].Close()
			}
		}
		s.connsLock.Unlock()
	}()

	// As this function only gets called when the exec request has WaitForWS enabled, we expect the client to
	// connect to all of the required websockets within a short period of time and we won't proceed until then.
	logger.Debug("Waiting for exec websockets to connect")
	select {
	case <-s.waitRequiredConnected.Done():
	case <-time.After(time.Second * 5):
		return errors.New("Timed out waiting for websockets to connect")
	}

	var err error
	var ttys []*os.File
	var ptys []*os.File

	var stdin *os.File
	var stdout *os.File
	var stderr *os.File

	if s.req.Interactive {
		if s.instance.Type() == instancetype.Container {
			// For containers, we setup a PTY on the LXD server.
			ttys = make([]*os.File, 1)
			ptys = make([]*os.File, 1)

			var rootUID, rootGID int64
			var devptsFd *os.File

			c, ok := s.instance.(instance.Container)
			if !ok {
				return errors.New("Invalid instance type")
			}

			idmapset, err := c.CurrentIdmap()
			if err != nil {
				return err
			}

			if idmapset != nil {
				rootUID, rootGID = idmapset.ShiftIntoNs(0, 0)
			}

			devptsFd, _ = c.DevptsFd()

			if devptsFd != nil && s.s.OS.NativeTerminals {
				ptys[0], ttys[0], err = shared.OpenPtyInDevpts(int(devptsFd.Fd()), rootUID, rootGID)
				_ = devptsFd.Close()
				devptsFd = nil
			} else {
				ptys[0], ttys[0], err = shared.OpenPty(rootUID, rootGID)
			}

			if err != nil {
				return fmt.Errorf("Unable to open the PTY device: %w", err)
			}

			stdin = ttys[0]
			stdout = ttys[0]
			stderr = ttys[0]

			if s.req.Width > 0 && s.req.Height > 0 {
				_ = shared.SetSize(int(ptys[0].Fd()), s.req.Width, s.req.Height)
			}
		} else {
			// For VMs we rely on the lxd-agent PTY running inside the VM guest.
			ttys = make([]*os.File, 2)
			ptys = make([]*os.File, 2)
			for i := range ttys {
				ptys[i], ttys[i], err = os.Pipe()
				if err != nil {
					return err
				}
			}

			stdin = ptys[execWSStdin]
			stdout = ttys[execWSStdout]
		}
	} else {
		ttys = make([]*os.File, 3)
		ptys = make([]*os.File, 3)
		for i := range ttys {
			ptys[i], ttys[i], err = os.Pipe()
			if err != nil {
				return err
			}
		}

		stdin = ptys[execWSStdin]
		stdout = ttys[execWSStdout]
		stderr = ttys[execWSStderr]
	}

	waitAttachedChildIsDead, markAttachedChildIsDead := context.WithCancel(context.Background())
	var wgEOF sync.WaitGroup

	// Define a function to clean up TTYs and sockets when done.
	finisher := func(cmdResult int, cmdErr error) error {
		// Cancel this before closing the control connection so control handler can detect command ending.
		markAttachedChildIsDead()

		for _, tty := range ttys {
			_ = tty.Close()
		}

		s.connsLock.Lock()
		conn := s.conns[execWSControl]
		s.connsLock.Unlock()

		if conn == nil {
			s.waitControlConnected.Cancel() // Request control go routine to end if no control connection.
		} else {
			err = conn.Close() // Close control connection (will cause control go routine to end).
			if err != nil && cmdErr == nil {
				cmdErr = err
			}
		}

		wgEOF.Wait()

		for _, pty := range ptys {
			_ = pty.Close()
		}

		// Make VM disconnections (shutdown/reboot) match containers.
		if cmdErr == drivers.ErrExecDisconnected {
			cmdResult = 129
			cmdErr = nil
		}

		metadata := shared.Jmap{"return": cmdResult}

		err = op.ExtendMetadata(metadata)
		if err != nil {
			return err
		}

		return cmdErr
	}

	cmd, err := s.instance.Exec(s.req, stdin, stdout, stderr)
	if err != nil {
		return finisher(-1, err)
	}

	l := logger.AddContext(logger.Ctx{"project": s.instance.Project().Name, "instance": s.instance.Name(), "PID": cmd.PID(), "interactive": s.req.Interactive})
	l.Debug("Instance process started")

	cmdKill := sync.OnceFunc(func() {
		err := cmd.Signal(unix.SIGKILL)
		if err != nil {
			l.Debug("Failed to send SIGKILL signal", logger.Ctx{"err": err})
		} else {
			l.Debug("Sent SIGKILL signal")
		}
	})

	// Now that process has started, we can start the control handler.
	wgEOF.Add(1)
	go func() {
		defer wgEOF.Done()

		<-s.waitControlConnected.Done() // Indicates control connection has started or command has ended.

		s.connsLock.Lock()
		conn := s.conns[execWSControl]
		s.connsLock.Unlock()

		if conn == nil {
			return // No connection, command has ended, being asked to end.
		}

		l.Debug("Exec control handler started")
		defer l.Debug("Exec control handler finished")

		for {
			mt, r, err := conn.NextReader()
			if err != nil || mt == websocket.CloseMessage {
				// Check if command process has finished normally, if so, no need to kill it.
				if waitAttachedChildIsDead.Err() != nil {
					return
				}

				if mt == websocket.CloseMessage {
					l.Warn("Got exec control websocket close message, killing command")
				} else {
					l.Warn("Failed getting exec control websocket reader, killing command", logger.Ctx{"err": err})
				}

				cmdKill()

				return
			}

			buf, err := io.ReadAll(r)
			if err != nil {
				// Check if command process has finished normally, if so, no need to kill it.
				if waitAttachedChildIsDead.Err() != nil {
					return
				}

				l.Warn("Failed reading control websocket message, killing command", logger.Ctx{"err": err})

				cmdKill()

				return
			}

			command := api.InstanceExecControl{}

			err = json.Unmarshal(buf, &command)
			if err != nil {
				l.Debug("Failed to unmarshal control socket command", logger.Ctx{"err": err})
				continue
			}

			// Only handle window-resize requests for interactive sessions.
			if command.Command == "window-resize" && s.req.Interactive {
				winchWidth, err := strconv.Atoi(command.Args["width"])
				if err != nil {
					l.Debug("Unable to extract window width", logger.Ctx{"err": err})
					continue
				}

				winchHeight, err := strconv.Atoi(command.Args["height"])
				if err != nil {
					l.Debug("Unable to extract window height", logger.Ctx{"err": err})
					continue
				}

				err = cmd.WindowResize(int(ptys[0].Fd()), winchWidth, winchHeight)
				if err != nil {
					l.Debug("Failed to set window size", logger.Ctx{"err": err, "width": winchWidth, "height": winchHeight})
					continue
				}
			} else if command.Command == "signal" {
				err := cmd.Signal(unix.Signal(command.Signal))
				if err != nil {
					l.Debug("Failed forwarding signal", logger.Ctx{"err": err, "signal": command.Signal})
					continue
				}
			}
		}
	}()

	// Now that process has started, we can start the mirroring of the process channels and websockets.
	if s.req.Interactive {
		wgEOF.Add(1)
		go func() {
			defer wgEOF.Done()

			var readErr, writeErr error
			l.Debug("Exec mirror websocket started", logger.Ctx{"number": 0})
			defer func() {
				l.Debug("Exec mirror websocket finished", logger.Ctx{"number": 0, "readErr": readErr, "writeErr": writeErr})
			}()

			s.connsLock.Lock()
			conn := s.conns[0]
			s.connsLock.Unlock()

			var readDone, writeDone chan error
			if s.instance.Type() == instancetype.Container {
				// For containers, we are running the command via the local LXD managed PTY and so
				// need to use the same PTY handle for both read and write.
				readDone, writeDone = ws.Mirror(conn, shared.NewExecWrapper(waitAttachedChildIsDead, ptys[0]))
			} else {
				readDone = ws.MirrorRead(conn, ptys[execWSStdout])
				writeDone = ws.MirrorWrite(conn, ttys[execWSStdin])
			}

			readErr = <-readDone
			writeErr = <-writeDone
			_ = conn.Close()
		}()
	} else {
		wgEOF.Add(len(ttys) - 1)
		for i := range ttys {
			go func(i int) {
				var err error
				l.Debug("Exec mirror websocket started", logger.Ctx{"number": i})
				defer func() {
					l.Debug("Exec mirror websocket finished", logger.Ctx{"number": i, "err": err})
				}()

				s.connsLock.Lock()
				conn := s.conns[i]
				s.connsLock.Unlock()

				if i == execWSStdout {
					// Launch a go routine that reads from stdout. This will be used to detect
					// when the client disconnects, as normally there should be no data
					// received on the stdout channel from the client. This is needed in cases
					// where the control connection isn't used by the client and so we need to
					// detect when the client disconnects to avoid leaving the command running
					// in the background.
					go func() {
						_, _, err := conn.ReadMessage() // Consume pings from server.

						// If there is a control connection, then leave it to that handler
						// to clean the command up. If there's no control connection, the
						// control context gets cancelled when the command exits, so this
						// can also be used indicate that the command has already finished.
						// In either case there is no need to kill the command, but if not
						// then it is our responsibility to kill the command now.
						if s.waitControlConnected.Err() == nil {
							l.Warn("Unexpected read on stdout websocket, killing command", logger.Ctx{"number": i, "err": err})
							cmdKill()
						}
					}()
				}

				if i == execWSStderr {
					// Consume data (e.g. websocket pings) from stderr too to
					// avoid a situation where we hit an inactivity timeout on
					// stderr during long exec sessions
					go func() {
						_, _, _ = conn.ReadMessage() // Consume pings from server.
					}()
				}

				if i == execWSStdin {
					err = <-ws.MirrorWrite(conn, ttys[i])
					_ = ttys[i].Close()
				} else {
					err = <-ws.MirrorRead(conn, shared.NewExecWrapper(waitAttachedChildIsDead, ptys[i]))
					_ = ptys[i].Close()
					wgEOF.Done()
				}
			}(i)
		}
	}

	exitStatus, err := cmd.Wait()
	l.Debug("Instance process stopped", logger.Ctx{"err": err, "exitStatus": exitStatus})

	return finisher(exitStatus, err)
}

// swagger:operation POST /1.0/instances/{name}/exec instances instance_exec_post
//
//	Run a command
//
//	Executes a command inside an instance.
//
//	The returned operation metadata will contain either 2 or 4 websockets.
//	In non-interactive mode, you'll get one websocket for each of stdin, stdout and stderr.
//	In interactive mode, a single bi-directional websocket is used for stdin and stdout/stderr.
//
//	An additional "control" socket is always added on top which can be used for out of band communication with LXD.
//	This allows sending signals and window sizing information through.
//
//	---
//	consumes:
//	  - application/json
//	produces:
//	  - application/json
//	parameters:
//	  - in: query
//	    name: project
//	    description: Project name
//	    type: string
//	    example: default
//	  - in: body
//	    name: exec
//	    description: Exec request
//	    schema:
//	      $ref: "#/definitions/InstanceExecPost"
//	responses:
//	  "202":
//	    $ref: "#/responses/Operation"
//	  "400":
//	    $ref: "#/responses/BadRequest"
//	  "403":
//	    $ref: "#/responses/Forbidden"
//	  "500":
//	    $ref: "#/responses/InternalServerError"
func instanceExecPost(d *Daemon, r *http.Request) response.Response {
	s := d.State()

	instanceType, err := urlInstanceTypeDetect(r)
	if err != nil {
		return response.SmartError(err)
	}

	projectName := request.ProjectParam(r)
	name, err := url.PathUnescape(mux.Vars(r)["name"])
	if err != nil {
		return response.SmartError(err)
	}

	if shared.IsSnapshot(name) {
		return response.BadRequest(errors.New("Invalid instance name"))
	}

	post := api.InstanceExecPost{}
	buf, err := io.ReadAll(r.Body)
	if err != nil {
		return response.BadRequest(err)
	}

	err = json.Unmarshal(buf, &post)
	if err != nil {
		return response.BadRequest(err)
	}

	// Constraint validations.
	if post.RecordOutput && post.WaitForWS {
		return response.BadRequest(fmt.Errorf("Cannot use %q in combination with %q", "record-output", "wait-for-websocket"))
	}

	if post.Interactive && post.RecordOutput {
		return response.BadRequest(fmt.Errorf("Cannot use %q in combination with %q", "interactive", "record-output"))
	}

	// Forward the request if the container is remote.
	client, err := cluster.ConnectIfInstanceIsRemote(r.Context(), s, projectName, name, instanceType)
	if err != nil {
		return response.SmartError(err)
	}

	if client != nil {
		url := api.NewURL().Path(version.APIVersion, "instances", name, "exec").Project(projectName)
		resp, _, err := client.RawQuery(http.MethodPost, url.String(), post, "")
		if err != nil {
			return response.SmartError(err)
		}

		opAPI, err := resp.MetadataAsOperation()
		if err != nil {
			return response.SmartError(err)
		}

		return operations.ForwardedOperationResponse(projectName, opAPI)
	}

	inst, err := instance.LoadByProjectAndName(s, projectName, name)
	if err != nil {
		return response.SmartError(err)
	}

	if !inst.IsRunning() {
		return response.BadRequest(errors.New("Instance is not running"))
	}

	if inst.IsFrozen() {
		return response.BadRequest(errors.New("Instance is frozen"))
	}

	// Process environment.
	if post.Environment == nil {
		post.Environment = map[string]string{}
	}

	// Override any environment variable settings from the instance if not manually specified in post.
	for k, v := range inst.ExpandedConfig() {
		envKey, found := strings.CutPrefix(k, "environment.")
		if found {
			_, found = post.Environment[envKey]
			if !found {
				post.Environment[envKey] = v
			}
		}
	}

	// Set default value for PATH.
	_, ok := post.Environment["PATH"]
	if !ok {
		post.Environment["PATH"] = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

		if inst.Type() == instancetype.Container {
			// Add some additional paths. This directly looks through /proc
			// rather than use FileExists as none of those paths are expected to be
			// symlinks and this is much faster than forking a sub-process and
			// attaching to the instance.
			extraPaths := map[string]string{
				"/snap":      "/snap/bin",
				"/etc/NIXOS": "/run/current-system/sw/bin",
			}

			instPID := inst.InitPID()
			for k, v := range extraPaths {
				if shared.PathExists(fmt.Sprintf("/proc/%d/root%s", instPID, k)) {
					post.Environment["PATH"] = fmt.Sprintf("%s:%s", post.Environment["PATH"], v)
				}
			}
		}
	}

	// If running as root, set some env variables.
	if post.User == 0 {
		// Set default value for HOME.
		_, ok = post.Environment["HOME"]
		if !ok {
			post.Environment["HOME"] = "/root"
		}

		// Set default value for USER.
		_, ok = post.Environment["USER"]
		if !ok {
			post.Environment["USER"] = "root"
		}
	}

	// Set default value for LANG.
	_, ok = post.Environment["LANG"]
	if !ok {
		post.Environment["LANG"] = "C.UTF-8"
	}

	if post.WaitForWS {
		ws := &execWs{}
		ws.s = d.State()
		ws.fds = map[int]string{}

		ws.conns = map[int]*websocket.Conn{}
		ws.conns[execWSControl] = nil
		ws.conns[0] = nil // This is used for either TTY or Stdin.
		if !post.Interactive {
			ws.conns[execWSStdout] = nil
			ws.conns[execWSStderr] = nil
		}

		ws.waitRequiredConnected = cancel.New()
		ws.waitControlConnected = cancel.New()

		for i := range ws.conns {
			ws.fds[i], err = shared.RandomCryptoString()
			if err != nil {
				return response.InternalError(err)
			}
		}

		ws.instance = inst
		ws.req = post

		resources := map[string][]api.URL{}
		resources["instances"] = []api.URL{*api.NewURL().Path(version.APIVersion, "instances", ws.instance.Name())}

		if ws.instance.Type() == instancetype.Container {
			resources["containers"] = resources["instances"]
		}

		op, err := operations.OperationCreate(r.Context(), s, projectName, operations.OperationClassWebsocket, operationtype.CommandExec, resources, ws.Metadata(), ws.Do, nil, ws.Connect)
		if err != nil {
			return response.InternalError(err)
		}

		return operations.OperationResponse(op)
	}

	run := func(op *operations.Operation) error {
		metadata := shared.Jmap{}

		var err error
		var stdout, stderr *os.File

		if post.RecordOutput {
			// Ensure exec-output directory exists
			execOutputDir := inst.ExecOutputPath()
			err = os.Mkdir(execOutputDir, 0600)
			if err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}

			// Prepare stdout and stderr recording.
			stdout, err = os.OpenFile(filepath.Join(execOutputDir, fmt.Sprintf("exec_%s.stdout", op.ID())), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0666)
			if err != nil {
				return err
			}

			defer func() { _ = stdout.Close() }()

			stderr, err = os.OpenFile(filepath.Join(execOutputDir, fmt.Sprintf("exec_%s.stderr", op.ID())), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0666)
			if err != nil {
				return err
			}

			defer func() { _ = stderr.Close() }()

			// Update metadata with the right URLs.
			metadata["output"] = shared.Jmap{
				"1": fmt.Sprintf("/%s/instances/%s/logs/exec-output/%s", version.APIVersion, inst.Name(), filepath.Base(stdout.Name())),
				"2": fmt.Sprintf("/%s/instances/%s/logs/exec-output/%s", version.APIVersion, inst.Name(), filepath.Base(stderr.Name())),
			}
		}

		// Run the command.
		cmd, err := inst.Exec(post, nil, stdout, stderr)
		if err != nil {
			return err
		}

		l := logger.AddContext(logger.Ctx{"project": inst.Project().Name, "instance": inst.Name(), "PID": cmd.PID(), "recordOutput": post.RecordOutput})
		l.Debug("Instance process started")

		exitStatus, cmdErr := cmd.Wait()
		l.Debug("Instance process stopped", logger.Ctx{"err": cmdErr, "exitStatus": exitStatus})

		metadata["return"] = exitStatus
		err = op.ExtendMetadata(metadata)
		if err != nil {
			l.Error("Error updating metadata for cmd", logger.Ctx{"err": err, "cmd": post.Command})
		}

		if cmdErr != nil {
			return cmdErr
		}

		return nil
	}

	resources := map[string][]api.URL{}
	resources["instances"] = []api.URL{*api.NewURL().Path(version.APIVersion, "instances", name)}

	if inst.Type() == instancetype.Container {
		resources["containers"] = resources["instances"]
	}

	op, err := operations.OperationCreate(r.Context(), s, projectName, operations.OperationClassTask, operationtype.CommandExec, resources, nil, run, nil, nil)
	if err != nil {
		return response.InternalError(err)
	}

	return operations.OperationResponse(op)
}
