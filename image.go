package main

import (
	"fmt"
	"os"
	"time"

	lxd "github.com/canonical/lxd/client"
	"github.com/canonical/lxd/shared"
	"github.com/canonical/lxd/shared/api"
)

const certPEM = "lxd-golden.pem"
const certCRT = "lxd-golden.crt"
const certKEY = "lxd-golden.key"

func ensureCertPresent() error {
	err := shared.FindOrGenCert(certCRT, certKEY, true, shared.CertOptions{})
	if err != nil {
		return err
	}

	crtData, err := os.ReadFile(certCRT)
	if err != nil {
		return err
	}

	keyData, err := os.ReadFile(certKEY)
	if err != nil {
		return err
	}

	// Combine CRT and KEY
	pemData := append(crtData, keyData...)

	// Check if PEM already exists and has the same content
	if existingData, err := os.ReadFile(certPEM); err == nil {
		if string(existingData) == string(pemData) {
			// No change, nothing to do
			return nil
		}
	}

	// Write the new PEM file
	err = os.WriteFile(certPEM, pemData, 0644)
	if err != nil {
		return err
	}

	return nil
}

// updateImage creates and publishes the golden image using the modern Instance API
func updateImage(d lxd.InstanceServer) error {
	const goldenInstanceName = "golden-instance"
	const goldenImageAlias = "golden"

	// 0. delete previous instance if exists
	instance, _, err := d.GetInstance(goldenInstanceName)
	if instance != nil && instance.Status == "Running" {
		stopReq := api.InstanceStatePut{
			Action:  "stop",
			Timeout: -1,
			Force:   true,
		}

		op, err := d.UpdateInstanceState(goldenInstanceName, stopReq, "")
		if err != nil {
			return fmt.Errorf("failed to stop instance: %w", err)
		}

		if err := op.Wait(); err != nil {
			return fmt.Errorf("error waiting for instance stop: %w", err)
		}
	}

	if err == nil {
		op, err := d.DeleteInstance(goldenInstanceName)
		if err != nil {
			return fmt.Errorf("failed to delete previous instance: %w", err)
		}

		if err := op.Wait(); err != nil {
			return fmt.Errorf("error waiting for instance deletion: %w", err)
		}
	}

	// 1. Launch minimal Ubuntu container
	req := api.InstancesPost{
		Name: goldenInstanceName,
		Type: "virtual-machine",
		InstancePut: api.InstancePut{
			Config: map[string]string{
				"limits.cpu":    "2",
				"limits.memory": "2GiB",
			},
			Profiles: []string{"default"},
		},
		Source: api.InstanceSource{
			Type:     "image",
			Alias:    "24.04",
			Mode:     "pull",
			Protocol: "simplestreams",
			Server:   "https://cloud-images.ubuntu.com/minimal/releases/",
		},
	}

	op, err := d.CreateInstance(req)
	if err != nil {
		return fmt.Errorf("failed to create instance: %w", err)
	}

	if err := op.Wait(); err != nil {
		return fmt.Errorf("failed waiting for instance creation: %w", err)
	}

	startReq := api.InstanceStatePut{
		Action:  "start",
		Timeout: -1,
		Force:   true,
	}

	op, err = d.UpdateInstanceState(goldenInstanceName, startReq, "")
	if err != nil {
		return fmt.Errorf("failed to start instance: %w", err)
	}

	if err := op.Wait(); err != nil {
		return fmt.Errorf("error waiting for instance start: %w", err)
	}

	err = waitForVmAgent(d, goldenInstanceName)
	if err != nil {
		return fmt.Errorf("failed waiting for VM agent: %w", err)
	}

	// 2. Prepare a single shell command to set up networking, install LXD, configure storage/network/profiles/auth
	certFile, err := os.ReadFile(certCRT)
	netplanYAML := `
network:
  version: 2
  ethernets:
    eth0:
      dhcp4: true
      dhcp6: true
`
	setupCmd := fmt.Sprintf(`
echo '%s' > /etc/netplan/10-lxd.yaml
chmod 0600 /etc/netplan/10-lxd.yaml
netplan apply
snap wait system seed.loaded
snap install lxd --channel=6/stable
lxc config set core.https_address [::]:8443
lxc config set user.ui_title "LXD Demo Server"
lxc storage create default dir
lxc network create lxdbr0
lxc profile device add default root disk path=/ pool=default
lxc profile device add default eth0 nic network=lxdbr0 name=eth0
echo '%s' > /var/snap/lxd/common/client.pem
lxc auth identity create tls/lxd-ui --group admins /var/snap/lxd/common/client.pem
rm /var/snap/lxd/common/client.pem
`, netplanYAML, certFile)

	execReq := api.InstanceExecPost{
		Command:     []string{"bash", "-c", setupCmd},
		WaitForWS:   true,
		Interactive: false,
	}

	execArgs := lxd.InstanceExecArgs{
		Stdin:    nil,
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
		DataDone: make(chan bool),
	}

	op, err = d.ExecInstance(goldenInstanceName, execReq, &execArgs)
	if err != nil {
		return fmt.Errorf("failed to setup instance: %w", err)
	}

	if err := op.Wait(); err != nil {
		return fmt.Errorf("error waiting for exec: %w", err)
	}

	// 3. Stop the instance
	stopReq := api.InstanceStatePut{
		Action:  "stop",
		Timeout: -1,
		Force:   true,
	}

	op, err = d.UpdateInstanceState(goldenInstanceName, stopReq, "")
	if err != nil {
		return fmt.Errorf("failed to stop instance: %w", err)
	}

	if err := op.Wait(); err != nil {
		return fmt.Errorf("error waiting for instance stop: %w", err)
	}

	// 4. Remove previous image
	images, err := d.GetImagesAllProjects()
	if err != nil {
		return fmt.Errorf("failed to list images: %w", err)
	}

	for _, img := range images {
		for _, alias := range img.Aliases {
			if alias.Name == goldenImageAlias {
				// Found previous golden image, delete it
				_, err = d.DeleteImage(img.Fingerprint)
				if err != nil {
					return fmt.Errorf("failed to delete previous golden image: %w", err)
				}

				break
			}
		}
	}

	// 5. Publish the golden image
	imageReq := api.ImagesPost{
		Source: &api.ImagesPostSource{
			Type: "instance",
			Name: goldenInstanceName,
		},
	}

	imageReq.Public = false
	op, err = d.CreateImage(imageReq, nil)
	if err != nil {
		return fmt.Errorf("failed to publish golden image: %w", err)
	}

	if err := op.Wait(); err != nil {
		return fmt.Errorf("error waiting for image publish: %w", err)
	}

	alias := api.ImageAliasesPost{}
	alias.Name = goldenImageAlias
	alias.Target = op.Get().Metadata["fingerprint"].(string)
	err = d.CreateImageAlias(alias)
	if err != nil {
		return fmt.Errorf("failed to create image alias: %w", err)
	}

	// 6. Clean up the instance
	op, err = d.DeleteInstance(goldenInstanceName)
	if err != nil {
		return fmt.Errorf("failed to delete instance: %w", err)
	}

	return nil
}

func waitForVmAgent(d lxd.InstanceServer, instanceName string) error {
	timeout := time.After(2 * time.Minute)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-timeout:
			return fmt.Errorf("timed out waiting for agent in %s", instanceName)
		case <-tick.C:
			state, _, err := d.GetInstanceState(instanceName)
			if err != nil {
				return fmt.Errorf("failed to get instance state: %w", err)
			}

			if state.Processes > 0 {
				// Agent is up, guest finished booting
				return nil
			}
		}
	}
}
