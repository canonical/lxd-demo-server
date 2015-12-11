package main

import (
	"github.com/lxc/lxd"
)

func lxdForceDelete(d *lxd.Client, name string) error {
	resp, err := d.Action(name, "stop", -1, true)
	if err == nil {
		d.WaitForSuccess(resp.Operation)
	}

	resp, err = d.Delete(name)
	if err != nil {
		return err
	}

	return d.WaitForSuccess(resp.Operation)
}
