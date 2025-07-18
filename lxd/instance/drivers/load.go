package drivers

import (
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/canonical/lxd/lxd/db"
	"github.com/canonical/lxd/lxd/db/cluster"
	"github.com/canonical/lxd/lxd/db/warningtype"
	"github.com/canonical/lxd/lxd/device"
	deviceConfig "github.com/canonical/lxd/lxd/device/config"
	"github.com/canonical/lxd/lxd/instance"
	"github.com/canonical/lxd/lxd/instance/instancetype"
	"github.com/canonical/lxd/lxd/project"
	"github.com/canonical/lxd/lxd/state"
	"github.com/canonical/lxd/shared/api"
	"github.com/canonical/lxd/shared/logger"
	"github.com/canonical/lxd/shared/revert"
)

// Instance driver definitions.
var instanceDrivers = map[string]func() instance.Instance{
	"lxc":  func() instance.Instance { return &lxc{} },
	"qemu": func() instance.Instance { return &qemu{} },
}

// DriverStatus definition.
type DriverStatus struct {
	Info      instance.Info
	Warning   *cluster.Warning
	Supported bool
}

// Supported instance drivers cache variables.
var driverStatusesMu sync.Mutex
var driverStatuses map[instancetype.Type]*DriverStatus

// Temporary instance reference storage (for hooks).
var instanceRefsMu sync.Mutex
var instanceRefs map[string]instance.Instance

func init() {
	// Expose load to the instance package, to avoid circular imports.
	instance.Load = load

	// Expose validDevices to the instance package, to avoid circular imports.
	instance.ValidDevices = validDevices

	// Expose create to the instance package, to avoid circular imports.
	instance.Create = create
}

// load creates the underlying instance type struct and returns it as an Instance.
func load(s *state.State, args db.InstanceArgs, p api.Project) (instance.Instance, error) {
	var inst instance.Instance
	var err error

	switch args.Type {
	case instancetype.Container:
		inst, err = lxcLoad(s, args, p)
	case instancetype.VM:
		inst, err = qemuLoad(s, args, p)
	default:
		return nil, fmt.Errorf("Invalid type for instance %q", args.Name)
	}

	if err != nil {
		return nil, err
	}

	return inst, nil
}

// validDevices validate instance device configs.
func validDevices(state *state.State, p api.Project, instanceType instancetype.Type, localDevices deviceConfig.Devices, expandedDevices deviceConfig.Devices) error {
	instConf := &common{
		dbType:          instanceType,
		localDevices:    localDevices.Clone(),
		expandedDevices: expandedDevices.Clone(),
		project:         p,
	}

	var checkedDevices []string

	checkDevices := func(devices deviceConfig.Devices, expanded bool) error {
		// Check each device individually using the device package.
		for deviceName, deviceConfig := range devices {
			if expanded && slices.Contains(checkedDevices, deviceName) {
				continue // Don't check the device twice if present in both local and expanded.
			}

			err := device.Validate(instConf, state, deviceName, deviceConfig)
			if err != nil {
				if expanded && errors.Is(err, device.ErrUnsupportedDevType) {
					// Skip unsupported devices in expanded config.
					// This allows mixed instance type profiles to be used where some devices
					// are only supported with specific instance types.
					continue
				}

				return fmt.Errorf("Device validation failed for %q: %w", deviceName, err)
			}

			checkedDevices = append(checkedDevices, deviceName)
		}

		return nil
	}

	// Check each local device individually using the device package.
	// Use the cloned config from instConf.localDevices so that the driver cannot modify it.
	err := checkDevices(instConf.localDevices, false)
	if err != nil {
		return err
	}

	if len(expandedDevices) > 0 {
		// Check we have a root disk if in expanded validation mode.
		_, _, err := instancetype.GetRootDiskDevice(expandedDevices.CloneNative())
		if err != nil {
			return fmt.Errorf("Failed detecting root disk device: %w", err)
		}

		// Check each expanded device individually using the device package.
		// Use the cloned config from instConf.expandedDevices so that the driver cannot modify it.
		err = checkDevices(instConf.expandedDevices, true)
		if err != nil {
			return err
		}
	}

	return nil
}

func create(s *state.State, args db.InstanceArgs, p api.Project) (instance.Instance, revert.Hook, error) {
	switch args.Type {
	case instancetype.Container:
		return lxcCreate(s, args, p)
	case instancetype.VM:
		return qemuCreate(s, args, p)
	}

	return nil, nil, errors.New("Instance type invalid")
}

// DriverStatuses returns a map of DriverStatus structs for all instance type drivers.
// The first time this function is called each of the instance drivers will be probed for support and the result
// will be cached internally to make subsequent calls faster.
func DriverStatuses() map[instancetype.Type]*DriverStatus {
	driverStatusesMu.Lock()
	defer driverStatusesMu.Unlock()

	if driverStatuses != nil {
		return driverStatuses
	}

	driverStatuses = make(map[instancetype.Type]*DriverStatus, len(instanceDrivers))

	for _, instanceDriver := range instanceDrivers {
		driverStatus := &DriverStatus{}

		driverInfo := instanceDriver().Info()
		driverStatus.Info = driverInfo
		driverStatus.Supported = true

		if driverInfo.Error != nil || driverInfo.Version == "" {
			logger.Warn("Instance type not operational", logger.Ctx{"type": driverInfo.Type, "driver": driverInfo.Name, "err": driverInfo.Error})

			driverStatus.Supported = false
			driverStatus.Warning = &cluster.Warning{
				TypeCode:    warningtype.InstanceTypeNotOperational,
				LastMessage: driverInfo.Error.Error(),
			}
		} else {
			logger.Info("Instance type operational", logger.Ctx{"type": driverInfo.Type, "driver": driverInfo.Name, "features": driverInfo.Features})
		}

		driverStatuses[driverInfo.Type] = driverStatus
	}

	return driverStatuses
}

// instanceRefGet retrieves an instance reference.
func instanceRefGet(projectName string, instName string) instance.Instance {
	instanceRefsMu.Lock()
	defer instanceRefsMu.Unlock()

	return instanceRefs[project.Instance(projectName, instName)]
}

// instanceRefSet stores a reference to an instance.
func instanceRefSet(inst instance.Instance) {
	instanceRefsMu.Lock()
	defer instanceRefsMu.Unlock()

	if instanceRefs == nil {
		instanceRefs = make(map[string]instance.Instance)
	}

	instanceRefs[project.Instance(inst.Project().Name, inst.Name())] = inst
}

// instanceRefClear removes an instance reference.
func instanceRefClear(inst instance.Instance) {
	instanceRefsMu.Lock()
	defer instanceRefsMu.Unlock()

	delete(instanceRefs, project.Instance(inst.Project().Name, inst.Name()))
}
