package ip

import (
	"context"
	"strings"

	"github.com/canonical/lxd/shared"
)

// Route represents arguments for route manipulation.
type Route struct {
	DevName string
	Route   string
	Table   string
	Src     string
	Proto   string
	Family  string
	Via     string
}

// Add adds new route.
func (r *Route) Add() error {
	cmd := []string{r.Family, "route", "add"}
	if r.Table != "" {
		cmd = append(cmd, "table", r.Table)
	}

	if r.Via != "" {
		cmd = append(cmd, "via", r.Via)
	}

	cmd = append(cmd, r.Route, "dev", r.DevName)
	if r.Src != "" {
		cmd = append(cmd, "src", r.Src)
	}

	if r.Proto != "" {
		cmd = append(cmd, "proto", r.Proto)
	}

	_, err := shared.RunCommandContext(context.TODO(), "ip", cmd...)
	if err != nil {
		return err
	}

	return nil
}

// Delete deletes routing table.
func (r *Route) Delete() error {
	_, err := shared.RunCommandContext(context.TODO(), "ip", r.Family, "route", "delete", "table", r.Table, r.Route, "dev", r.DevName)
	if err != nil {
		return err
	}

	return nil
}

// Flush flushes routing tables.
func (r *Route) Flush() error {
	cmd := []string{}
	if r.Family != "" {
		cmd = append(cmd, r.Family)
	}

	cmd = append(cmd, "route", "flush")
	if r.Route != "" {
		cmd = append(cmd, r.Route)
	}

	if r.Via != "" {
		cmd = append(cmd, "via", r.Via)
	}

	cmd = append(cmd, "dev", r.DevName)
	if r.Proto != "" {
		cmd = append(cmd, "proto", r.Proto)
	}

	_, err := shared.RunCommandContext(context.TODO(), "ip", cmd...)
	if err != nil {
		return err
	}

	return nil
}

// Replace changes or adds new route.
func (r *Route) Replace(routes []string) error {
	cmd := []string{r.Family, "route", "replace", "dev", r.DevName, "proto", r.Proto}
	cmd = append(cmd, routes...)
	_, err := shared.RunCommandContext(context.TODO(), "ip", cmd...)
	if err != nil {
		return err
	}

	return nil
}

// Show lists routes.
func (r *Route) Show() ([]string, error) {
	routes := []string{}
	out, err := shared.RunCommandContext(context.TODO(), "ip", r.Family, "route", "show", "dev", r.DevName, "proto", r.Proto)
	if err != nil {
		return routes, err
	}

	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		route := strings.ReplaceAll(line, "linkdown", "")
		routes = append(routes, route)
	}

	return routes, nil
}
