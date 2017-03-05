# LXD demo server

This repository contains the backend code of the LXD online demo service.

[https://linuxcontainers.org/lxd/try-it](https://linuxcontainers.org/lxd/try-it)

## What is it

Simply put, it's a small Go daemon exposing a REST API that users
(mostly our javascript client) can interact with to create temporary
test containers and attach to that container's console.

Those containers come with a bunch of resource limitations and an
expiry, when the container expires, it's automatically deleted.

The main client can be found at the URL above, with its source available here:  
[https://github.com/lxc/linuxcontainers.org](https://github.com/lxc/linuxcontainers.org)

## Installing on Ubuntu
The easiest way to get the demo server running on Ubuntu is by using the snap package.

First install and configure LXD itself:

```
sudo snap install lxd
sudo lxd init
```

Then install and configure the LXD demo server:

```
sudo snap install lxd-demo-server
sudo snap connect lxd-demo-server:lxd lxd:lxd
sudo lxd-demo-server.configure
```

You can then access the server at: http://IP-ADDRESS:8080/

## Dependencies

The server needs to be able to talk to a LXD daemon over the local unix
socket, so you need to have a LXD daemon installed and functional before
using this server.

Other than that, you can pull all the other necessary dependencies with:

    go get github.com/lxc/lxd-demo-server

## Building it

A very simple:

    go build

Should do the trick.

## Running it

To run your own, you should start by copying the example configuration
file "lxd-demo.yaml.example" to "lxd-demo.yaml", then update its content
according to your environment.

You will either need a container to copy for every request or a
container image to use, set that up and set the appropriate
configuration key.

Once done, simply run the daemon with:

    ./lxd-demo-server

The daemon isn't verbose at all, in fact it will only log critical LXD errors.

You can test things with:

    curl http://localhost:8080/1.0
    curl http://localhost:8080/1.0/terms

The server monitors the current directory for changes to its configuration file.
It will automatically reload the configuration after it's changed.

## Bug reports

Bug reports can be filed at https://github.com/lxc/lxd-demo-server/issues/new

## Contributing

Fixes and new features are greatly appreciated but please read our
[contributing guidelines](CONTRIBUTING.md) first.

Contributions to this project should be sent as pull requests on github.

## Support and discussions

We use the LXC mailing-lists for developer and user discussions, you can
find and subscribe to those at: https://lists.linuxcontainers.org

If you prefer live discussions, some of us also hang out in
[#lxcontainers](http://webchat.freenode.net/?channels=#lxcontainers) on irc.freenode.net.
