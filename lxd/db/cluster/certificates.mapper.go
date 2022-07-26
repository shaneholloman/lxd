//go:build linux && cgo && !agent

package cluster

// The code below was generated by lxd-generate - DO NOT EDIT!

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"

	"github.com/lxc/lxd/lxd/db/query"
	"github.com/lxc/lxd/shared/api"
)

var _ = api.ServerEnvironment{}

var certificateObjects = RegisterStmt(`
SELECT certificates.id, certificates.fingerprint, certificates.type, certificates.name, certificates.certificate, certificates.restricted
  FROM certificates
  ORDER BY certificates.fingerprint
`)

var certificateObjectsByID = RegisterStmt(`
SELECT certificates.id, certificates.fingerprint, certificates.type, certificates.name, certificates.certificate, certificates.restricted
  FROM certificates
  WHERE certificates.id = ? ORDER BY certificates.fingerprint
`)

var certificateObjectsByFingerprint = RegisterStmt(`
SELECT certificates.id, certificates.fingerprint, certificates.type, certificates.name, certificates.certificate, certificates.restricted
  FROM certificates
  WHERE certificates.fingerprint = ? ORDER BY certificates.fingerprint
`)

var certificateID = RegisterStmt(`
SELECT certificates.id FROM certificates
  WHERE certificates.fingerprint = ?
`)

var certificateCreate = RegisterStmt(`
INSERT INTO certificates (fingerprint, type, name, certificate, restricted)
  VALUES (?, ?, ?, ?, ?)
`)

var certificateDeleteByFingerprint = RegisterStmt(`
DELETE FROM certificates WHERE fingerprint = ?
`)

var certificateDeleteByNameAndType = RegisterStmt(`
DELETE FROM certificates WHERE name = ? AND type = ?
`)

var certificateUpdate = RegisterStmt(`
UPDATE certificates
  SET fingerprint = ?, type = ?, name = ?, certificate = ?, restricted = ?
 WHERE id = ?
`)

// GetCertificates returns all available certificates.
// generator: certificate GetMany
func GetCertificates(ctx context.Context, tx *sql.Tx, filter CertificateFilter) ([]Certificate, error) {
	var err error

	// Result slice.
	objects := make([]Certificate, 0)

	// Pick the prepared statement and arguments to use based on active criteria.
	var sqlStmt *sql.Stmt
	var args []any

	if filter.ID != nil && filter.Fingerprint == nil && filter.Name == nil && filter.Type == nil {
		sqlStmt = Stmt(tx, certificateObjectsByID)
		args = []any{
			filter.ID,
		}
	} else if filter.Fingerprint != nil && filter.ID == nil && filter.Name == nil && filter.Type == nil {
		sqlStmt = Stmt(tx, certificateObjectsByFingerprint)
		args = []any{
			filter.Fingerprint,
		}
	} else if filter.ID == nil && filter.Fingerprint == nil && filter.Name == nil && filter.Type == nil {
		sqlStmt = Stmt(tx, certificateObjects)
		args = []any{}
	} else {
		return nil, fmt.Errorf("No statement exists for the given Filter")
	}

	// Dest function for scanning a row.
	dest := func(i int) []any {
		objects = append(objects, Certificate{})
		return []any{
			&objects[i].ID,
			&objects[i].Fingerprint,
			&objects[i].Type,
			&objects[i].Name,
			&objects[i].Certificate,
			&objects[i].Restricted,
		}
	}

	// Select.
	err = query.SelectObjects(sqlStmt, dest, args...)
	if err != nil {
		return nil, fmt.Errorf("Failed to fetch from \"certificates\" table: %w", err)
	}

	return objects, nil
}

// GetCertificate returns the certificate with the given key.
// generator: certificate GetOne
func GetCertificate(ctx context.Context, tx *sql.Tx, fingerprint string) (*Certificate, error) {
	filter := CertificateFilter{}
	filter.Fingerprint = &fingerprint

	objects, err := GetCertificates(ctx, tx, filter)
	if err != nil {
		return nil, fmt.Errorf("Failed to fetch from \"certificates\" table: %w", err)
	}

	switch len(objects) {
	case 0:
		return nil, api.StatusErrorf(http.StatusNotFound, "Certificate not found")
	case 1:
		return &objects[0], nil
	default:
		return nil, fmt.Errorf("More than one \"certificates\" entry matches")
	}
}

// GetCertificateID return the ID of the certificate with the given key.
// generator: certificate ID
func GetCertificateID(ctx context.Context, tx *sql.Tx, fingerprint string) (int64, error) {
	stmt := Stmt(tx, certificateID)
	rows, err := stmt.Query(fingerprint)
	if err != nil {
		return -1, fmt.Errorf("Failed to get \"certificates\" ID: %w", err)
	}

	defer func() { _ = rows.Close() }()

	// Ensure we read one and only one row.
	if !rows.Next() {
		return -1, api.StatusErrorf(http.StatusNotFound, "Certificate not found")
	}

	var id int64
	err = rows.Scan(&id)
	if err != nil {
		return -1, fmt.Errorf("Failed to scan ID: %w", err)
	}

	if rows.Next() {
		return -1, fmt.Errorf("More than one row returned")
	}

	err = rows.Err()
	if err != nil {
		return -1, fmt.Errorf("Result set failure: %w", err)
	}

	return id, nil
}

// CertificateExists checks if a certificate with the given key exists.
// generator: certificate Exists
func CertificateExists(ctx context.Context, tx *sql.Tx, fingerprint string) (bool, error) {
	_, err := GetCertificateID(ctx, tx, fingerprint)
	if err != nil {
		if api.StatusErrorCheck(err, http.StatusNotFound) {
			return false, nil
		}

		return false, err
	}

	return true, nil
}

// CreateCertificate adds a new certificate to the database.
// generator: certificate Create
func CreateCertificate(ctx context.Context, tx *sql.Tx, object Certificate) (int64, error) {
	// Check if a certificate with the same key exists.
	exists, err := CertificateExists(ctx, tx, object.Fingerprint)
	if err != nil {
		return -1, fmt.Errorf("Failed to check for duplicates: %w", err)
	}

	if exists {
		return -1, api.StatusErrorf(http.StatusConflict, "This \"certificates\" entry already exists")
	}

	args := make([]any, 5)

	// Populate the statement arguments.
	args[0] = object.Fingerprint
	args[1] = object.Type
	args[2] = object.Name
	args[3] = object.Certificate
	args[4] = object.Restricted

	// Prepared statement to use.
	stmt := Stmt(tx, certificateCreate)

	// Execute the statement.
	result, err := stmt.Exec(args...)
	if err != nil {
		return -1, fmt.Errorf("Failed to create \"certificates\" entry: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return -1, fmt.Errorf("Failed to fetch \"certificates\" entry ID: %w", err)
	}

	return id, nil
}

// DeleteCertificate deletes the certificate matching the given key parameters.
// generator: certificate DeleteOne-by-Fingerprint
func DeleteCertificate(ctx context.Context, tx *sql.Tx, fingerprint string) error {
	stmt := Stmt(tx, certificateDeleteByFingerprint)
	result, err := stmt.Exec(fingerprint)
	if err != nil {
		return fmt.Errorf("Delete \"certificates\": %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("Fetch affected rows: %w", err)
	}

	if n == 0 {
		return api.StatusErrorf(http.StatusNotFound, "Certificate not found")
	} else if n > 1 {
		return fmt.Errorf("Query deleted %d Certificate rows instead of 1", n)
	}

	return nil
}

// DeleteCertificates deletes the certificate matching the given key parameters.
// generator: certificate DeleteMany-by-Name-and-Type
func DeleteCertificates(ctx context.Context, tx *sql.Tx, name string, certificateType CertificateType) error {
	stmt := Stmt(tx, certificateDeleteByNameAndType)
	result, err := stmt.Exec(name, certificateType)
	if err != nil {
		return fmt.Errorf("Delete \"certificates\": %w", err)
	}

	_, err = result.RowsAffected()
	if err != nil {
		return fmt.Errorf("Fetch affected rows: %w", err)
	}

	return nil
}

// UpdateCertificate updates the certificate matching the given key parameters.
// generator: certificate Update
func UpdateCertificate(ctx context.Context, tx *sql.Tx, fingerprint string, object Certificate) error {
	id, err := GetCertificateID(ctx, tx, fingerprint)
	if err != nil {
		return err
	}

	stmt := Stmt(tx, certificateUpdate)
	result, err := stmt.Exec(object.Fingerprint, object.Type, object.Name, object.Certificate, object.Restricted, id)
	if err != nil {
		return fmt.Errorf("Update \"certificates\" entry failed: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("Fetch affected rows: %w", err)
	}

	if n != 1 {
		return fmt.Errorf("Query updated %d rows instead of 1", n)
	}

	return nil
}
