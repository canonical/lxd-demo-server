package main

import (
	"github.com/canonical/lxd/client"
	"github.com/canonical/lxd/shared/api"
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
