package main

import (
	"github.com/lxc/lxd/client"
	"github.com/lxc/lxd/shared/api"
)

func lxdForceDelete(d lxd.ContainerServer, name string) error {
	req := api.ContainerStatePut{
		Action:  "stop",
		Timeout: -1,
		Force:   true,
	}

	op, err := d.UpdateContainerState(name, req, "")
	if err == nil {
		op.Wait()
	}

	op, err = d.DeleteContainer(name)
	if err != nil {
		return err
	}

	return op.Wait()
}
