package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/ovh/cds/sdk"
	"github.com/ovh/cds/sdk/grpcplugin"
	"github.com/ovh/cds/sdk/log"
)

type startGRPCPluginOptions struct {
	out  io.Writer
	err  io.Writer
	envs []string
}

type pluginClientSocket struct {
	Socket  string
	BuffOut bytes.Buffer
	Client  interface{}
}

func enablePluginLogger(ctx context.Context, sendLog LoggerFunc, c *pluginClientSocket) {
	var accumulator string
	var shouldExit bool
	defer func() {
		if accumulator != "" {
			sendLog(accumulator)
		}
	}()

	for {
		if ctx.Err() != nil {
			shouldExit = true
		}

		b, err := c.BuffOut.ReadByte()
		if err == io.EOF {
			if shouldExit {
				return
			}
			continue
		}

		content := string(b)
		switch content {
		case "":
			continue
		case "\n":
			accumulator += content
			sendLog(accumulator)
			accumulator = ""
			continue
		default:
			accumulator += content
			continue
		}
	}
}

func startGRPCPlugin(ctx context.Context, pluginName string, w *currentWorker, p *sdk.GRPCPluginBinary, opts startGRPCPluginOptions) (*pluginClientSocket, error) {
	currentOS := strings.ToLower(sdk.GOOS)
	currentARCH := strings.ToLower(sdk.GOARCH)
	pluginSocket, has := w.mapPluginClient[pluginName]
	if has {
		return pluginSocket, nil
	}

	binary := p
	if binary == nil {
		var errBi error
		binary, errBi = w.client.PluginGetBinaryInfos(pluginName, currentOS, currentARCH)
		if errBi != nil || binary == nil {
			return nil, sdk.WrapError(errBi, "Unable to get plugin binary infos... Aborting")
		}
	}

	c := pluginClientSocket{}

	mOut := io.MultiWriter(opts.out, &c.BuffOut)
	mErr := io.MultiWriter(opts.err, &c.BuffOut)
	dir := w.currentJob.workingDirectory
	if dir == "" {
		dir = w.basedir
	}

	envs := make([]string, 0, len(opts.envs))
	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "CDS_") {
			continue
		}
		envs = append(envs, env)
	}
	envs = append(envs, opts.envs...)

	log.Info("Starting GRPC Plugin %s in dir %s", binary.Name, dir)
	fileContent, err := ioutil.ReadFile(path.Join(w.basedir, binary.GetName()))
	if err != nil {
		return nil, sdk.WrapError(err, "Unable to get plugin binary file... Aborting")
	}

	switch {
	case sdk.IsTar(fileContent):
		if err := sdk.Untar(w.basedir, bytes.NewReader(fileContent)); err != nil {
			return nil, sdk.WrapError(err, "Unable to untar binary file")
		}
	case sdk.IsGz(fileContent):
		if err := sdk.UntarGz(w.basedir, bytes.NewReader(fileContent)); err != nil {
			return nil, sdk.WrapError(err, "Unable to untarGz binary file")
		}
	}

	for i := range binary.Entrypoints {
		binary.Entrypoints[i] = path.Join(w.basedir, binary.Entrypoints[i])
	}

	cmd := binary.Cmd
	if _, err := exec.LookPath(cmd); err != nil {
		cmd = path.Join(w.basedir, cmd)
		_, err = exec.LookPath(cmd)
		if err != nil {
			return nil, sdk.WrapError(err, "Unable to start GRPC plugin, binary command not found.")
		}
	}
	args := append(binary.Entrypoints, binary.Args...)

	if err := grpcplugin.StartPlugin(ctx, dir, cmd, args, envs, mOut, mErr); err != nil {
		return nil, sdk.WrapError(err, "Unable to start GRPC plugin... Aborting")
	}
	log.Info("GRPC Plugin %s started", binary.Name)

	//Sleep a while, to let the plugin write on stdout the socket address
	time.Sleep(500 * time.Millisecond)
	tsStart := time.Now()

	buff := new(strings.Builder)
	for {
		b, err := c.BuffOut.ReadByte()
		if err != nil && len(buff.String()) > 0 {
			buff.Reset()
			if time.Now().Before(tsStart.Add(5 * time.Second)) {
				log.Warning("Error on ReadByte, retry in 500ms...")
				time.Sleep(500 * time.Millisecond)
				continue
			}
			log.Error("error on ReadByte(len buff %d, content: %s): %v", len(buff.String()), buff.String(), err)
			return nil, fmt.Errorf("unable to get socket address from started binary")
		}
		if err := buff.WriteByte(b); err != nil {
			log.Error("error on write byte: %v", err)
			break
		}
		if strings.HasSuffix(buff.String(), "is ready to accept new connection\n") {
			break
		}
	}
	socket := strings.TrimSpace(strings.Replace(buff.String(), " is ready to accept new connection\n", "", 1))
	log.Info("socket %s ready", socket)

	c.Socket = socket
	registerPluginClient(w, pluginName, &c)

	return &c, nil
}

func registerPluginClient(w *currentWorker, pluginName string, c *pluginClientSocket) {
	w.mapPluginClient[pluginName] = c
}
