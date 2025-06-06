package ip

import (
	"context"

	"github.com/canonical/lxd/shared"
)

// Class represents qdisc class object.
type Class struct {
	Dev     string
	Parent  string
	Classid string
}

// ClassHTB represents htb qdisc class object.
type ClassHTB struct {
	Class
	Rate string
}

// Add adds class to a node.
func (class *ClassHTB) Add() error {
	cmd := []string{"class", "add", "dev", class.Dev, "parent", class.Parent}
	if class.Classid != "" {
		cmd = append(cmd, "classid", class.Classid)
	}

	cmd = append(cmd, "htb")
	if class.Rate != "" {
		cmd = append(cmd, "rate", class.Rate)
	}

	_, err := shared.RunCommandContext(context.TODO(), "tc", cmd...)
	if err != nil {
		return err
	}

	return nil
}
