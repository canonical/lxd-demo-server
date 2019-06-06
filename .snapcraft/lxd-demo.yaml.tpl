# Source of the container
## Either an image (local or remote)
image: ubuntu:18.04
## Or an existing container
#container: some-container

# Profile to use for the container
profiles:
    - default

# Command to spawn in the container
command: ["bash"]

# Enable the feedback API
feedback: true
feedback_timeout: 30

# Resource limitations
quota_cpu: 1
quota_processes: 200
quota_ram: 256
quota_sessions: 2
quota_time: 1800
## Disk quotas only work when using btrfs or zfs
#quota_disk: 5

server_addr: "[::]:8080"
server_banned_ips: []
server_console_only: true
server_containers_max: 50
server_ipv6_only: false
server_maintenance: false
server_statistics_keys:
 - UUID
server_terms: |-
  By using the LXD demonstration server, you agree that:<br />
  <ul>
    <li>Access to this service may be revoked at any time for any reason</li>
    <li>Access to this service is solely provided to evaluate LXD</li>
    <li>Your IP address, access time and activity on the test server may be recorded</li>
    <li>Any abuse of this service may lead to a ban or other applicable actions</li>
  </ul>
