package device

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	deviceConfig "github.com/canonical/lxd/lxd/device/config"
	"github.com/canonical/lxd/lxd/idmap"
	"github.com/canonical/lxd/lxd/state"
	"github.com/canonical/lxd/lxd/storage/filesystem"
	"github.com/canonical/lxd/shared"
	"github.com/canonical/lxd/shared/logger"
)

// unixDefaultMode default mode to create unix devices with if not specified in device config.
const unixDefaultMode = 0660

// unixDeviceAttributes returns the device type, major and minor numbers for a device.
func unixDeviceAttributes(path string) (dType string, major uint32, minor uint32, err error) {
	// Get a stat struct from the provided path
	stat := unix.Stat_t{}
	err = unix.Stat(path, &stat)
	if err != nil {
		return "", 0, 0, err
	}

	// Check what kind of file it is
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFBLK:
		dType = "b"
	case unix.S_IFCHR:
		dType = "c"
	default:
		return "", 0, 0, errors.New("Not a device")
	}

	// Return the device information
	major = unix.Major(uint64(stat.Rdev))
	minor = unix.Minor(uint64(stat.Rdev))

	return dType, major, minor, nil
}

// unixDeviceOwnership returns the ownership (gid and uid) for a device.
func unixDeviceOwnership(path string) (gid uint32, uid uint32, err error) {
	stat := unix.Stat_t{}
	err = unix.Stat(path, &stat)
	if err != nil {
		return 0, 0, err
	}

	gid = stat.Gid
	uid = stat.Uid

	return gid, uid, nil
}

// unixDeviceModeOct converts a string unix octal mode to an int.
func unixDeviceModeOct(strmode string) (int, error) {
	i, err := strconv.ParseInt(strmode, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("Bad device mode: %s", strmode)
	}

	return int(i), nil
}

// UnixDevice contains information about a created UNIX device.
type UnixDevice struct {
	HostPath     string      // Absolute path to the device on the host.
	RelativePath string      // Relative path where the device will be mounted inside instance.
	Type         string      // Type of device; c (for char) or b for (block).
	Major        uint32      // Major number.
	Minor        uint32      // Minor number.
	Mode         os.FileMode // File mode.
	UID          int         // Owner UID.
	GID          int         // Owner GID.
}

// unixDeviceSourcePath returns the absolute path for a device on the host.
// This is based on the "source" property of the device's config, or the "path" property if "source"
// not define. This uses the shared.HostPath function so works when running in a snap environment.
func unixDeviceSourcePath(m deviceConfig.Device) string {
	srcPath := m["source"]
	if srcPath == "" {
		srcPath = m["path"]
	}

	return shared.HostPath(srcPath)
}

// unixDeviceDestPath returns the absolute path for a device inside an instance.
// This is based on the "path" property of the device's config, or the "source" property if "path"
// not defined.
func unixDeviceDestPath(m deviceConfig.Device) string {
	destPath := m["path"]
	if destPath == "" {
		destPath = m["source"]
	}

	return destPath
}

// UnixDeviceCreate creates a UNIX device (either block or char). If the supplied device config map
// contains a major and minor number for the device, then a stat is avoided, otherwise this info
// retrieved from the origin device. Similarly, if a mode is supplied in the device config map or
// defaultMode is set as true, then the device is created with the supplied or default mode (0660)
// respectively, otherwise the origin device's mode is used. If the device config doesn't contain a
// type field then it defaults to created a unix-char device. The ownership of the created device
// defaults to root (0) but can be specified with the uid and gid fields in the device config map. If ownership.inherit is set to true, the device ownership is inherited from the host.
// It returns a UnixDevice containing information about the device created.
func UnixDeviceCreate(s *state.State, idmapSet *idmap.IdmapSet, devicesPath string, prefix string, m deviceConfig.Device, defaultMode bool) (*UnixDevice, error) {
	var err error
	d := UnixDevice{}

	// Extra checks for nesting.
	deviceProperties := []string{"major", "minor", "mode", "uid", "gid"}
	if s.OS.RunningInUserNS {
		for key, value := range m {
			if value != "" && slices.Contains(deviceProperties, key) {
				return nil, fmt.Errorf("The %q property may not be set when adding a device to a nested container", key)
			}
		}
	}

	srcPath := unixDeviceSourcePath(m)

	// Get the major/minor of the device we want to create.
	if m["major"] == "" && m["minor"] == "" {
		// If no major and minor are set, use those from the device on the host.
		_, d.Major, d.Minor, err = unixDeviceAttributes(srcPath)
		if err != nil {
			return nil, fmt.Errorf("Failed to get device attributes for %q: %w", srcPath, err)
		}
	} else if m["major"] == "" || m["minor"] == "" {
		return nil, fmt.Errorf("Both major and minor must be supplied for device: %s", srcPath)
	} else {
		tmp, err := strconv.ParseUint(m["major"], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("Bad major %q in device %q", m["major"], srcPath)
		}

		d.Major = uint32(tmp)

		tmp, err = strconv.ParseUint(m["minor"], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("Bad minor %q in device %q", m["minor"], srcPath)
		}

		d.Minor = uint32(tmp)
	}

	// Get the device mode (defaults to unixDefaultMode if not supplied).
	d.Mode = os.FileMode(unixDefaultMode)
	if m["mode"] != "" {
		tmp, err := unixDeviceModeOct(m["mode"])
		if err != nil {
			return nil, fmt.Errorf("Bad mode %q in device %q", m["mode"], srcPath)
		}

		d.Mode = os.FileMode(tmp)
	} else if !defaultMode {
		// If not specified mode in device config, and default mode is false, then try and
		// read the source device's mode and use that inside the instance.
		d.Mode, err = shared.GetPathMode(srcPath)
		if err != nil {
			errno, isErrno := shared.GetErrno(err)
			if !isErrno || errno != unix.ENOENT {
				return nil, fmt.Errorf("Failed to retrieve mode of device %q: %w", srcPath, err)
			}

			d.Mode = os.FileMode(unixDefaultMode)
		}
	}

	if m["type"] == "unix-block" {
		d.Mode |= unix.S_IFBLK
		d.Type = "b"
	} else {
		d.Mode |= unix.S_IFCHR
		d.Type = "c"
	}

	// Get the device owner.
	if shared.IsTrue(m["ownership.inherit"]) {
		if m["uid"] == "" {
			_, uid, err := unixDeviceOwnership(srcPath)
			if err != nil {
				return nil, fmt.Errorf("Failed to retrieve host UID of device %q: %w", srcPath, err)
			}

			d.UID = int(uid)
		}

		if m["gid"] == "" {
			gid, _, err := unixDeviceOwnership(srcPath)
			if err != nil {
				return nil, fmt.Errorf("Failed to retrieve host GID of device %q: %w", srcPath, err)
			}

			d.GID = int(gid)
		}
	} else {
		if m["uid"] != "" {
			d.UID, err = strconv.Atoi(m["uid"])
			if err != nil {
				return nil, fmt.Errorf("Invalid UID %q in device %q", m["uid"], srcPath)
			}
		}

		if m["gid"] != "" {
			d.GID, err = strconv.Atoi(m["gid"])
			if err != nil {
				return nil, fmt.Errorf("Invalid GID %q in device %q", m["gid"], srcPath)
			}
		}
	}

	// Create the devices directory if missing.
	if !shared.PathExists(devicesPath) {
		err := os.Mkdir(devicesPath, 0711)
		if err != nil {
			return nil, fmt.Errorf("Failed to create devices path: %s", err)
		}
	}

	destPath := unixDeviceDestPath(m)
	relativeDestPath := strings.TrimPrefix(destPath, "/")
	devName := filesystem.PathNameEncode(deviceJoinPath(prefix, relativeDestPath))
	devPath := filepath.Join(devicesPath, devName)

	// Create the new entry.
	if !s.OS.RunningInUserNS {
		if s.OS.Nodev {
			return nil, errors.New("Can't create device as devices path is mounted nodev")
		}

		devNum := int(unix.Mkdev(d.Major, d.Minor))
		err := unix.Mknod(devPath, uint32(d.Mode), devNum)
		if err != nil {
			return nil, fmt.Errorf("Failed to create device %q for %q: %w", devPath, srcPath, err)
		}

		err = os.Chown(devPath, d.UID, d.GID)
		if err != nil {
			return nil, fmt.Errorf("Failed to chown device %q: %w", devPath, err)
		}

		// Needed as mknod respects the umask.
		err = os.Chmod(devPath, d.Mode)
		if err != nil {
			return nil, fmt.Errorf("Failed to chmod device %q: %w", devPath, err)
		}

		if idmapSet != nil {
			err := idmapSet.ShiftFile(devPath)
			if err != nil {
				// uidshift failing is weird, but not a big problem. Log and proceed.
				logger.Debugf("Failed to uidshift device %q: %s\n", srcPath, err)
			}
		}
	} else {
		f, err := os.Create(devPath)
		if err != nil {
			return nil, err
		}

		_ = f.Close()

		err = DiskMount(srcPath, devPath, false, "", nil, "none")
		if err != nil {
			return nil, err
		}
	}

	d.HostPath = devPath
	d.RelativePath = relativeDestPath
	return &d, nil
}

// unixDeviceSetup creates a UNIX device on host and then configures supplied RunConfig with the
// mount and cgroup rule instructions to have it be attached to the instance. If defaultMode is true
// or mode is supplied in the device config then the origin device does not need to be accessed for
// its file mode.
func unixDeviceSetup(s *state.State, devicesPath string, typePrefix string, deviceName string, m deviceConfig.Device, defaultMode bool, runConf *deviceConfig.RunConfig) error {
	// Before creating the device, check that another existing device isn't using the same mount
	// path inside the instance as our device. If we find an existing device with the same mount
	// path we will skip mounting our device inside the instance. This can happen when multiple
	// LXD devices share the same parent device (such as Nvidia GPUs and Infiniband devices).

	// Convert the requested dest path inside the instance to an encoded relative one.
	ourDestPath := unixDeviceDestPath(m)
	ourEncRelDestFile := filesystem.PathNameEncode(strings.TrimPrefix(ourDestPath, "/"))

	// Load all existing host devices.
	dents, err := os.ReadDir(devicesPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	}

	dupe := false
	for _, ent := range dents {
		devName := ent.Name()

		// Remove the LXD device type and name prefix, leaving just the encoded dest path.
		idx := strings.LastIndex(devName, ".")
		if idx == -1 {
			return fmt.Errorf("Invalid device name %q", devName)
		}

		encRelDestFile := devName[idx+1:]

		// If the encoded relative path of the device file matches the encoded relative dest
		// path of our new device then return as we do not want to instruct LXD to mount
		// the device and create cgroup rules.
		if encRelDestFile == ourEncRelDestFile {
			dupe = true // There is an existing device using the same mount path.
			break
		}
	}

	// Create the device on the host.
	ourPrefix := deviceJoinPath(typePrefix, deviceName)
	d, err := UnixDeviceCreate(s, nil, devicesPath, ourPrefix, m, defaultMode)
	if err != nil {
		return err
	}

	// If there was an existing device using the same mount path detected then skip mounting.
	if dupe {
		return nil
	}

	// Instruct LXD to perform the mount.
	runConf.Mounts = append(runConf.Mounts, deviceConfig.MountEntryItem{
		DevSource:  deviceConfig.DevSourcePath{Path: d.HostPath},
		TargetPath: d.RelativePath,
		FSType:     "none",
		Opts:       []string{"bind", "create=file"},
		OwnerShift: deviceConfig.MountOwnerShiftStatic,
	})

	// Instruct LXD to setup the cgroup rule.
	runConf.CGroups = append(runConf.CGroups, deviceConfig.RunConfigItem{
		Key:   "devices.allow",
		Value: fmt.Sprintf("%s %d:%d rwm", d.Type, d.Major, d.Minor),
	})

	return nil
}

// unixDeviceSetupCharNum calls unixDeviceSetup and overrides the supplied device config with the
// type as "unix-char" and the supplied major and minor numbers. This function can be used when you
// already know the device's major and minor numbers to avoid unixDeviceSetup() having to stat the
// device to ascertain these attributes. If defaultMode is true or mode is supplied in the device
// config then the origin device does not need to be accessed for its file mode.
func unixDeviceSetupCharNum(s *state.State, devicesPath string, typePrefix string, deviceName string, m deviceConfig.Device, major uint32, minor uint32, path string, defaultMode bool, runConf *deviceConfig.RunConfig) error {
	configCopy := m.Clone()

	// Overriding these in the config copy should avoid the need for unixDeviceSetup to stat
	// the origin device to ascertain this information.
	configCopy["type"] = "unix-char"
	configCopy["major"] = strconv.FormatUint(uint64(major), 10)
	configCopy["minor"] = strconv.FormatUint(uint64(minor), 10)
	configCopy["path"] = path

	return unixDeviceSetup(s, devicesPath, typePrefix, deviceName, configCopy, defaultMode, runConf)
}

// unixDeviceSetupBlockNum calls unixDeviceSetup and overrides the supplied device config with the
// type as "unix-block" and the supplied major and minor numbers. This function can be used when you
// already know the device's major and minor numbers to avoid unixDeviceSetup() having to stat the
// device to ascertain these attributes. If defaultMode is true or mode is supplied in the device
// config then the origin device does not need to be accessed for its file mode.
func unixDeviceSetupBlockNum(s *state.State, devicesPath string, typePrefix string, deviceName string, m deviceConfig.Device, major uint32, minor uint32, path string, defaultMode bool, runConf *deviceConfig.RunConfig) error {
	configCopy := m.Clone()

	// Overriding these in the config copy should avoid the need for unixDeviceSetup to stat
	// the origin device to ascertain this information.
	configCopy["type"] = "unix-block"
	configCopy["major"] = strconv.FormatUint(uint64(major), 10)
	configCopy["minor"] = strconv.FormatUint(uint64(minor), 10)
	configCopy["path"] = path

	return unixDeviceSetup(s, devicesPath, typePrefix, deviceName, configCopy, defaultMode, runConf)
}

// UnixDeviceExists checks if the unix device already exists in devices path.
func UnixDeviceExists(devicesPath string, prefix string, path string) bool {
	relativeDestPath := strings.TrimPrefix(path, "/")
	devName := fmt.Sprintf("%s.%s", filesystem.PathNameEncode(prefix), filesystem.PathNameEncode(relativeDestPath))
	devPath := filepath.Join(devicesPath, devName)

	return shared.PathExists(devPath)
}

// unixRemoveDevice identifies all files related to the supplied typePrefix and deviceName and then
// populates the supplied runConf with the instructions to remove cgroup rules and unmount devices.
// It detects if any other devices attached to the instance that share the same prefix have the same
// relative mount path inside the instance encoded into the file name. If there is another device
// that shares the same mount path then the unmount rule is not added to the runConf as the device
// may still be in use with another LXD device.
// Accepts an optional file prefix that will be used to narrow the selection of files to remove.
func unixDeviceRemove(devicesPath string, typePrefix string, deviceName string, optPrefix string, runConf *deviceConfig.RunConfig) error {
	// Load all devices.
	dents, err := os.ReadDir(devicesPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	}

	var ourPrefix string
	// If a prefix override has been supplied, use that for filtering the devices to remove.
	if optPrefix != "" {
		ourPrefix = filesystem.PathNameEncode(deviceJoinPath(typePrefix, deviceName, optPrefix))
	} else {
		ourPrefix = filesystem.PathNameEncode(deviceJoinPath(typePrefix, deviceName))
	}

	ourDevs := []string{}
	otherDevs := []string{}

	for _, ent := range dents {
		devName := ent.Name()

		// This device file belongs our LXD device.
		if strings.HasPrefix(devName, ourPrefix) {
			ourDevs = append(ourDevs, devName)
			continue
		}

		// This device file belongs to another LXD device.
		otherDevs = append(otherDevs, devName)
	}

	// It is possible for some LXD devices to share the same device on the same mount point
	// inside the instance. We extract the relative path of the device that is encoded into its
	// name on the host so that we can compare the device files for our own device and check
	// none of them use the same mount point.
	encRelDevFiles := []string{}
	for _, otherDev := range otherDevs {
		// Remove the LXD device type and name prefix, leaving just the encoded dest path.
		idx := strings.LastIndex(otherDev, ".")
		if idx == -1 {
			return fmt.Errorf("Invalid device name %q", otherDev)
		}

		encRelDestFile := otherDev[idx+1:]
		encRelDevFiles = append(encRelDevFiles, encRelDestFile)
	}

	// Check that none of our devices are in use by another LXD device.
	for _, ourDev := range ourDevs {
		// Remove the LXD device type and name prefix, leaving just the encoded dest path.
		idx := strings.LastIndex(ourDev, ".")
		if idx == -1 {
			return fmt.Errorf("Invalid device name %q", ourDev)
		}

		ourEncRelDestFile := ourDev[idx+1:]

		// Look for devices for other LXD devices that match the same path.
		dupe := slices.Contains(encRelDevFiles, ourEncRelDestFile)

		// If a device has been found that points to the same device inside the instance
		// then we cannot request it be umounted inside the instance as it's still in use.
		if dupe {
			continue
		}

		// Append this device to the mount rules (these will be unmounted).
		runConf.Mounts = append(runConf.Mounts, deviceConfig.MountEntryItem{
			TargetPath: filesystem.PathNameDecode(ourEncRelDestFile),
		})

		absDevPath := filepath.Join(devicesPath, ourDev)
		dType, dMajor, dMinor, err := unixDeviceAttributes(absDevPath)
		if err != nil {
			return fmt.Errorf("Failed to get UNIX device attributes for %q: %w", absDevPath, err)
		}

		// Append a deny cgroup fule for this device.
		runConf.CGroups = append(runConf.CGroups, deviceConfig.RunConfigItem{
			Key:   "devices.deny",
			Value: fmt.Sprintf("%s %d:%d rwm", dType, dMajor, dMinor),
		})
	}

	return nil
}

// unixDeviceDeleteFiles removes all host side device files for a particular LXD device.
// Accepts an optional file prefix that will be used to narrow the selection of files to delete.
// This should be run after the files have been detached from the instance as a post hook.
func unixDeviceDeleteFiles(s *state.State, devicesPath string, typePrefix string, deviceName string, optPrefix string) error {
	var ourPrefix string
	// If a prefix override has been supplied, use that for filtering the devices to remove.
	if optPrefix != "" {
		ourPrefix = filesystem.PathNameEncode(deviceJoinPath(typePrefix, deviceName, optPrefix))
	} else {
		ourPrefix = filesystem.PathNameEncode(deviceJoinPath(typePrefix, deviceName))
	}

	// Load all devices.
	dents, err := os.ReadDir(devicesPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	}

	// Remove our host side device files.
	for _, ent := range dents {
		devName := ent.Name()

		// This device file belongs our LXD device.
		if strings.HasPrefix(devName, ourPrefix) {
			devPath := filepath.Join(devicesPath, devName)

			// Remove the host side mount.
			if s.OS.RunningInUserNS {
				_ = unix.Unmount(devPath, unix.MNT_DETACH)
			}

			// Remove the host side device file.
			err := os.Remove(devPath)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// unixValidDeviceNum validates the major and minor numbers for a UNIX device.
func unixValidDeviceNum(value string) error {
	if value == "" {
		return nil
	}

	_, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return errors.New("Invalid value for a UNIX device number")
	}

	return nil
}

// unixValidUserID validates the UNIX UID and GID values for ownership.
func unixValidUserID(value string) error {
	if value == "" {
		return nil
	}

	_, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return errors.New("Invalid value for a UNIX ID")
	}

	return nil
}

// unixValidOctalFileMode validates the UNIX file mode.
func unixValidOctalFileMode(value string) error {
	if value == "" {
		return nil
	}

	_, err := strconv.ParseUint(value, 8, 32)
	if err != nil {
		return errors.New("Invalid value for an octal file mode")
	}

	return nil
}
