package main

import (
	"fmt"
	"math/rand"
	"strings"

	"github.com/lxc/lxd"
	"github.com/lxc/lxd/shared"
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

func getCPURange() string {
	available := config.ServerCPUCount
	wanted := config.QuotaCPU
	if wanted > available {
		wanted = available
	}

	var cpus []string
	for wanted != 0 {
		var cpu string
		if available-1 != 0 {
			cpu = fmt.Sprintf("%d", rand.Intn(available-1))
		} else {
			cpu = "0"
		}

		if shared.StringInSlice(cpu, cpus) {
			continue
		}

		cpus = append(cpus, cpu)
		wanted--
	}

	return strings.Join(cpus, ",")
}
