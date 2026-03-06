/*
Copyright 2026 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package docker contains helpers for working with Docker-compatible container
// runtimes.
package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/docker/cli/cli/config"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// Check attempts to connect to the local Docker daemon (or any
// Docker-compatible runtime) and returns an error if it's unable to do so.
func Check(ctx context.Context) error {
	cli, err := newClient()
	if err != nil {
		return err
	}
	if _, err := cli.Ping(ctx); err != nil {
		return errors.Wrap(err, "failed to ping docker daemon")
	}

	return nil
}

// GetContainerIDByName returns the ID of the container with the given name. If
// includeStopped is true, non-running containers are included in the search.
func GetContainerIDByName(ctx context.Context, name string, includeStopped bool) (string, bool, error) {
	c, found, err := GetContainerByName(ctx, name, includeStopped)
	if err != nil {
		return "", false, err
	}

	if !found {
		return "", false, nil
	}

	return c.ID, true, nil
}

// GetContainerByName returns the container with the given name. If
// includeStopped is true, non-running containers are included in the search.
func GetContainerByName(ctx context.Context, name string, includeStopped bool) (*container.Summary, bool, error) {
	cli, err := newClient()
	if err != nil {
		return nil, false, err
	}

	cs, err := cli.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(filters.KeyValuePair{Key: "name", Value: name}),
		All:     includeStopped,
	})
	if err != nil {
		return nil, false, errors.Wrap(err, "failed to list containers")
	}

	if len(cs) == 0 {
		return nil, false, nil
	}

	return &cs[0], true, nil
}

// GetNetworkIDByName returns the ID of the network with the given name.
func GetNetworkIDByName(ctx context.Context, name string) (string, bool, error) {
	cli, err := newClient()
	if err != nil {
		return "", false, err
	}

	ns, err := cli.NetworkList(ctx, network.ListOptions{
		Filters: filters.NewArgs(filters.KeyValuePair{Key: "name", Value: name}),
	})
	if err != nil {
		return "", false, errors.Wrap(err, "failed to list networks")
	}

	if len(ns) == 0 {
		return "", false, nil
	}

	return ns[0].ID, true, nil
}

// StartContainer starts a container with the given name using the given
// image. Additional options can be provided via StartContainerOption. The ID of
// the started container is returned.
func StartContainer(ctx context.Context, name, img string, opts ...StartContainerOption) (string, error) {
	cfg := &startContainerConfig{
		containerConfig: &container.Config{
			Image: img,
		},
	}
	for _, opt := range opts {
		opt(cfg)
	}

	cli, err := newClient()
	if err != nil {
		return "", err
	}

	// Pull the image if needed.
	if _, err := cli.ImageInspect(ctx, img); err != nil {
		auth, err := defaultRegistryAuth(img)
		if err != nil {
			return "", err
		}

		out, err := cli.ImagePull(ctx, img, image.PullOptions{
			RegistryAuth: auth,
		})
		if err != nil {
			return "", errors.Wrapf(err, "failed to pull image %q", img)
		}

		// Ensure the image pull is complete by reading the output stream.
		if _, err := io.Copy(io.Discard, out); err != nil {
			return "", errors.Wrapf(err, "failed to read image pull output for %s", img)
		}
	}

	resp, err := cli.ContainerCreate(ctx,
		cfg.containerConfig,
		cfg.hostConfig,
		nil,
		nil,
		name,
	)
	if err != nil {
		return "", errors.Wrap(err, "failed to create container")
	}

	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", errors.Wrap(err, "failed to start container")
	}

	// Connect to additional networks.
	for _, nid := range cfg.networks {
		if err := cli.NetworkConnect(ctx, nid, resp.ID, nil); err != nil {
			return "", errors.Wrapf(err, "failed to connect container to network %q", nid)
		}
	}

	return resp.ID, nil
}

func defaultRegistryAuth(imageName string) (string, error) {
	hostname := resolveRegistryFromImage(imageName)
	cfg, err := config.Load(config.Dir())
	if err != nil {
		return "", err
	}

	auth, err := cfg.GetAuthConfig(hostname)
	if err != nil {
		return "", err
	}

	data, err := json.Marshal(auth)
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(data), nil
}

func resolveRegistryFromImage(img string) string {
	parts := strings.Split(img, "/")
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

// StartContainerByID starts an existing container by ID. It is idempotent in
// that no error is returned if the given container is already running.
func StartContainerByID(ctx context.Context, id string) error {
	cli, err := newClient()
	if err != nil {
		return err
	}

	if err := cli.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		return errors.Wrap(err, "failed to start container")
	}

	return nil
}

type startContainerConfig struct {
	containerConfig *container.Config
	hostConfig      *container.HostConfig
	networks        []string
}

// StartContainerOption provides optional options for StartContainer.
type StartContainerOption func(*startContainerConfig)

// StartWithCommand sets the command to use when starting a container.
func StartWithCommand(cmd []string) StartContainerOption {
	return func(cfg *startContainerConfig) {
		if cfg.containerConfig == nil {
			cfg.containerConfig = &container.Config{}
		}
		cfg.containerConfig.Cmd = cmd
	}
}

// StartWithBindMount adds a bind mount when starting a container.
func StartWithBindMount(hostPath, containerPath string) StartContainerOption {
	return func(cfg *startContainerConfig) {
		if cfg.hostConfig == nil {
			cfg.hostConfig = &container.HostConfig{}
		}
		cfg.hostConfig.Binds = append(cfg.hostConfig.Binds, fmt.Sprintf("%s:%s", hostPath, containerPath))
	}
}

// StartWithNetworkID adds a network to which a container should be added when
// starting it.
func StartWithNetworkID(nid string) StartContainerOption {
	return func(cfg *startContainerConfig) {
		cfg.networks = append(cfg.networks, nid)
	}
}

// StopContainerByID stops and removes a container. It will not return an error
// if the container is already stopped.
func StopContainerByID(ctx context.Context, cid string) error {
	cli, err := newClient()
	if err != nil {
		return err
	}

	if err := cli.ContainerStop(ctx, cid, container.StopOptions{}); err != nil {
		return errors.Wrap(err, "failed to stop container")
	}
	if err := cli.ContainerRemove(ctx, cid, container.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
		return errors.Wrap(err, "failed to remove container")
	}

	return nil
}

func newClient() (*client.Client, error) {
	cli, err := client.NewClientWithOpts(client.WithAPIVersionNegotiation(), client.FromEnv)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create docker client")
	}

	return cli, nil
}
