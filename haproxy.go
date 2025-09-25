package main

import (
	"fmt"
	"os"
	"os/exec"
)

const cfgPath = "haproxy.cfg"
const pidPath = "haproxy.pid"

func startHAProxy() error {
	err := generateHAProxyConfig()
	if err != nil {
		return fmt.Errorf("generate haproxy config: %w", err)
	}

	cmd := exec.Command("haproxy", "-f", cfgPath, "-p", pidPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start haproxy: %w", err)
	}

	// Reap the process in a goroutine to avoid defunct
	go func() {
		cmd.Wait()
	}()

	return nil
}

func updateHAProxy() error {
	err := generateHAProxyConfig()
	if err != nil {
		return fmt.Errorf("generate haproxy config: %w", err)
	}

	pid, err := os.ReadFile(pidPath)
	if err != nil {
		return fmt.Errorf("read pid file: %w", err)
	}

	// reload haproxy gracefully
	args := []string{"-f", cfgPath, "-p", pidPath, "-sf", string(pid)}
	cmd := exec.Command("haproxy", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("reload haproxy: %w", err)
	}

	return nil
}

func generateHAProxyConfig() error {
	containers, err := dbActive()
	if err != nil {
		return fmt.Errorf("read active containers: %w", err)
	}

	var haproxyFrontends, haproxyBackends string

	for _, entry := range containers {
		containerId := int64(entry[0].(int))
		uuid := entry[3].(string)
		ip := entry[4].(string)

		haproxyFrontends += fmt.Sprintf(`
    acl is_lxd_%d req.hdr(x-orig-host) -m sub %s
    use_backend lxd_%d if is_lxd_%d`, containerId, uuid, containerId, containerId)

		haproxyBackends += fmt.Sprintf(`

backend lxd_%d
    server lxd_https_%d %s:8443 ssl verify none crt %s`, containerId, containerId, ip, certPEM)
	}

	// base haproxy.cfg template
	config := fmt.Sprintf(`
global
    daemon

defaults
    mode	http
    timeout connect 50000
    timeout client  500000
    timeout server  500000

frontend lxd_frontend
    bind 0.0.0.0:80%s
    use_backend lxd_demo_site

backend lxd_demo_site
    server lxd_demo 127.0.0.1:8080%s
`, haproxyFrontends, haproxyBackends)

	// write out haproxy.cfg
	if err := os.WriteFile(cfgPath, []byte(config), 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	return nil
}
