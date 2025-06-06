//go:build linux && cgo && !agent

package cluster

// The code below was generated by lxd-generate - DO NOT EDIT!

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/canonical/lxd/lxd/db/query"
	"github.com/canonical/lxd/shared/api"
)

var _ = api.ServerEnvironment{}

const deviceObjects = `SELECT %s_devices.id, %s_devices.%s_id, %s_devices.name, %s_devices.type
  FROM %s_devices
  ORDER BY %s_devices.name`

const deviceCreate = `INSERT INTO %s_devices (%s_id, name, type)
  VALUES (?, ?, ?)`

const deviceDelete = `DELETE FROM %s_devices WHERE %s_id = ?`

// deviceColumns returns a string of column names to be used with a SELECT statement for the entity.
// Use this function when building statements to retrieve database entries matching the Device entity.
func deviceColumns() string {
	return "%s_devices.id, %s_devices.%s_id, %s_devices.name, %s_devices.type, %s_devices.config"
}

// getDevices can be used to run handwritten sql.Stmts to return a slice of objects.
func getDevices(ctx context.Context, stmt *sql.Stmt, parent string, args ...any) ([]Device, error) {
	objects := make([]Device, 0)

	dest := func(scan func(dest ...any) error) error {
		d := Device{}
		err := scan(&d.ID, &d.ReferenceID, &d.Name, &d.Type)
		if err != nil {
			return err
		}

		objects = append(objects, d)

		return nil
	}

	err := query.SelectObjects(ctx, stmt, dest, args...)
	if err != nil {
		return nil, fmt.Errorf("Failed to fetch from \"%s_devices\" table: %w", parent, err)
	}

	return objects, nil
}

// getDevicesRaw can be used to run handwritten query strings to return a slice of objects.
func getDevicesRaw(ctx context.Context, tx *sql.Tx, sql string, parent string, args ...any) ([]Device, error) {
	objects := make([]Device, 0)

	dest := func(scan func(dest ...any) error) error {
		d := Device{}
		err := scan(&d.ID, &d.ReferenceID, &d.Name, &d.Type)
		if err != nil {
			return err
		}

		objects = append(objects, d)

		return nil
	}

	err := query.Scan(ctx, tx, sql, dest, args...)
	if err != nil {
		return nil, fmt.Errorf("Failed to fetch from \"%s_devices\" table: %w", parent, err)
	}

	return objects, nil
}

// GetDevices returns all available devices for the parent entity.
// generator: device GetMany
func GetDevices(ctx context.Context, tx *sql.Tx, parent string, filters ...DeviceFilter) (map[int][]Device, error) {
	var err error

	// Result slice.
	objects := make([]Device, 0)

	deviceObjectsLocal := strings.ReplaceAll(deviceObjects, "%s_id", parent+"_id")
	fillParent := make([]any, strings.Count(deviceObjectsLocal, "%s"))
	mangledParent := strings.ReplaceAll(parent, "_", "s_") + "s"
	for i := range fillParent {
		fillParent[i] = mangledParent
	}

	queryStr := fmt.Sprintf(deviceObjectsLocal, fillParent...)
	queryParts := strings.SplitN(queryStr, "ORDER BY", 2)
	args := []any{}

	for i, filter := range filters {
		var cond string
		if i == 0 {
			cond = " WHERE ( %s )"
		} else {
			cond = " OR ( %s )"
		}

		entries := []string{}
		if filter.Name != nil {
			entries = append(entries, "name = ?")
			args = append(args, filter.Name)
		}

		if filter.Type != nil {
			entries = append(entries, "type = ?")
			args = append(args, filter.Type)
		}

		if len(entries) == 0 {
			return nil, errors.New("Cannot filter on empty DeviceFilter")
		}

		queryParts[0] += fmt.Sprintf(cond, strings.Join(entries, " AND "))
	}

	queryStr = strings.Join(queryParts, " ORDER BY")
	// Select.
	objects, err = getDevicesRaw(ctx, tx, queryStr, parent, args...)
	if err != nil {
		return nil, fmt.Errorf("Failed to fetch from \"%s_devices\" table: %w", parent, err)
	}

	configFilters := []ConfigFilter{}
	for _, f := range filters {
		filter := f.Config
		if filter != nil {
			if filter.Key == nil && filter.Value == nil {
				return nil, errors.New("Cannot filter on empty ConfigFilter")
			}

			configFilters = append(configFilters, *filter)
		}
	}

	config, err := GetConfig(ctx, tx, parent+"_device", configFilters...)
	if err != nil {
		return nil, err
	}

	for i := range objects {
		_, ok := config[objects[i].ID]
		if !ok {
			objects[i].Config = map[string]string{}
		} else {
			objects[i].Config = config[objects[i].ID]
		}
	}

	resultMap := map[int][]Device{}
	for _, object := range objects {
		_, ok := resultMap[object.ReferenceID]
		if !ok {
			resultMap[object.ReferenceID] = []Device{}
		}

		resultMap[object.ReferenceID] = append(resultMap[object.ReferenceID], object)
	}

	return resultMap, nil
}

// CreateDevices adds a new device to the database.
// generator: device Create
func CreateDevices(ctx context.Context, tx *sql.Tx, parent string, objects map[string]Device) error {
	deviceCreateLocal := strings.ReplaceAll(deviceCreate, "%s_id", parent+"_id")
	fillParent := make([]any, strings.Count(deviceCreateLocal, "%s"))
	for i := range fillParent {
		fillParent[i] = strings.ReplaceAll(parent, "_", "s_") + "s"
	}

	queryStr := fmt.Sprintf(deviceCreateLocal, fillParent...)
	for _, object := range objects {
		result, err := tx.ExecContext(ctx, queryStr, object.ReferenceID, object.Name, object.Type)
		if err != nil {
			return fmt.Errorf("Insert failed for \"%s_devices\" table: %w", parent, err)
		}

		id, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("Failed to fetch ID: %w", err)
		}

		referenceID := int(id)
		for key, value := range object.Config {
			insert := Config{
				ReferenceID: referenceID,
				Key:         key,
				Value:       value,
			}

			err = CreateConfig(ctx, tx, parent+"_device", insert)
			if err != nil {
				return fmt.Errorf("Insert Config failed for Device: %w", err)
			}
		}
	}

	return nil
}

// UpdateDevices updates the device matching the given key parameters.
// generator: device Update
func UpdateDevices(ctx context.Context, tx *sql.Tx, parent string, referenceID int, devices map[string]Device) error {
	// Delete current entry.
	err := DeleteDevices(ctx, tx, parent, referenceID)
	if err != nil {
		return err
	}

	// Insert new entries.
	for key, object := range devices {
		object.ReferenceID = referenceID
		devices[key] = object
	}

	err = CreateDevices(ctx, tx, parent, devices)
	if err != nil {
		return err
	}

	return nil
}

// DeleteDevices deletes the device matching the given key parameters.
// generator: device DeleteMany
func DeleteDevices(ctx context.Context, tx *sql.Tx, parent string, referenceID int) error {
	deviceDeleteLocal := strings.ReplaceAll(deviceDelete, "%s_id", parent+"_id")
	fillParent := make([]any, strings.Count(deviceDeleteLocal, "%s"))
	for i := range fillParent {
		fillParent[i] = strings.ReplaceAll(parent, "_", "s_") + "s"
	}

	queryStr := fmt.Sprintf(deviceDeleteLocal, fillParent...)
	result, err := tx.ExecContext(ctx, queryStr, referenceID)
	if err != nil {
		return fmt.Errorf("Delete entry for \"%s_device\" failed: %w", parent, err)
	}

	_, err = result.RowsAffected()
	if err != nil {
		return fmt.Errorf("Fetch affected rows: %w", err)
	}

	return nil
}
