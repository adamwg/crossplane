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
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/docker/cli/cli/config"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// Check attempts to connect to the local Docker daemon and returns an error if
// it's unable to do so.
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

// GetContainerIDByName returns the ID of the container with the given name.
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

// GetContainerByName returns the container with the given name.
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

// StartContainer starts a container with the given name using the given image.
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

	for path, tarball := range cfg.copyFiles {
		if err := cli.CopyToContainer(ctx, resp.ID, filepath.Clean(path), bytes.NewReader(tarball), container.CopyToContainerOptions{}); err != nil {
			return "", errors.Wrapf(err, "failed to copy files to container path %s", path)
		}
	}

	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", errors.Wrap(err, "failed to start container")
	}

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

// StartContainerByID starts an existing container by ID.
func StartContainerByID(ctx context.Context, id string) error {
	cli, err := newClient()
	if err != nil {
		return err
	}
	return errors.Wrap(cli.ContainerStart(ctx, id, container.StartOptions{}), "failed to start container")
}

type startContainerConfig struct {
	containerConfig *container.Config
	hostConfig      *container.HostConfig
	networks        []string
	copyFiles       map[string][]byte
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

// StartWithNetworkID adds a network to which a container should be added.
func StartWithNetworkID(nid string) StartContainerOption {
	return func(cfg *startContainerConfig) {
		cfg.networks = append(cfg.networks, nid)
	}
}

// StartWithCopyFiles adds files that should be copied to the given path before
// starting the container.
func StartWithCopyFiles(tarball []byte, path string) StartContainerOption {
	return func(cfg *startContainerConfig) {
		if cfg.copyFiles == nil {
			cfg.copyFiles = make(map[string][]byte)
		}
		cfg.copyFiles[path] = tarball
	}
}

// StartWithEnv adds environment variables that will be passed to the container.
func StartWithEnv(env ...string) StartContainerOption {
	return func(cfg *startContainerConfig) {
		cfg.containerConfig.Env = append(cfg.containerConfig.Env, env...)
	}
}

// StartWithWorkingDirectory sets the working directory for the container.
func StartWithWorkingDirectory(path string) StartContainerOption {
	return func(cfg *startContainerConfig) {
		cfg.containerConfig.WorkingDir = path
	}
}

// StopContainerByID stops and removes a container.
func StopContainerByID(ctx context.Context, cid string) error {
	cli, err := newClient()
	if err != nil {
		return err
	}

	if err := cli.ContainerStop(ctx, cid, container.StopOptions{}); err != nil {
		return errors.Wrap(err, "failed to stop container")
	}
	return errors.Wrap(
		cli.ContainerRemove(ctx, cid, container.RemoveOptions{Force: true, RemoveVolumes: true}),
		"failed to remove container",
	)
}

// WaitForContainerByID waits for the container with the given ID to stop.
func WaitForContainerByID(ctx context.Context, cid string) error {
	cli, err := newClient()
	if err != nil {
		return err
	}

	statusCh, errCh := cli.ContainerWait(ctx, cid, container.WaitConditionNotRunning)
	select {
	case status := <-statusCh:
		if status.StatusCode != 0 {
			out, err := cli.ContainerLogs(ctx, cid, container.LogsOptions{ShowStdout: true, ShowStderr: true})
			if err != nil {
				return errors.Wrapf(err, "failed to get container logs")
			}

			logs := new(strings.Builder)
			if _, err := io.Copy(logs, out); err != nil {
				return errors.Wrapf(err, "failed to read container logs")
			}

			return fmt.Errorf("container exited with non-zero status: %d, logs: %s", status.StatusCode, logs.String())
		}
	case err := <-errCh:
		return errors.Wrapf(err, "container unknown failure")
	}

	return nil
}

// CopyFromContainer copies files from a container to an afero filesystem.
func CopyFromContainer(ctx context.Context, cid, path string, fs afero.Fs) error {
	cli, err := newClient()
	if err != nil {
		return err
	}

	reader, _, err := cli.CopyFromContainer(ctx, cid, path)
	if err != nil {
		return errors.Wrap(err, "failed to copy files from container")
	}

	tarReader := tar.NewReader(reader)
	const maxFileSize = 10 * 1024 * 1024

	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errors.Wrap(err, "failed while reading tarball")
		}

		cleanedPath := filepath.Clean(strings.TrimPrefix(header.Name, filepath.Base(path)+"/"))

		switch header.Typeflag {
		case tar.TypeDir:
			if err := fs.MkdirAll(cleanedPath, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			outFile, err := fs.Create(cleanedPath)
			if err != nil {
				return err
			}

			limitedReader := io.LimitReader(tarReader, maxFileSize)
			if _, err := io.Copy(outFile, limitedReader); err != nil {
				if cerr := outFile.Close(); cerr != nil {
					err = errors.Wrap(cerr, "error while closing file")
				}
				return err
			}
			if cerr := outFile.Close(); cerr != nil {
				return errors.Wrapf(cerr, "error closing file %s", header.Name)
			}
		}
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
