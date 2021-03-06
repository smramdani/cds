package grpcplugin

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ovh/cds/sdk"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

type Plugin interface {
	Start(context.Context) error
	Stop()
	Instance() *Common
}

func StartPlugin(ctx context.Context, workdir, cmd string, args []string, env []string, stdout, stderr io.Writer) error {
	c := exec.CommandContext(ctx, cmd, args...)
	c.Dir = workdir
	c.Env = env
	c.Stdout = stdout
	c.Stderr = stderr
	return c.Start()
}

type Common struct {
	Desc   *grpc.ServiceDesc
	Srv    interface{}
	Socket string
	s      *grpc.Server
}

func (c *Common) Instance() *Common {
	return c
}

func (c *Common) Start(ctx context.Context) error {
	_, err := c.start(ctx, c.Desc, c.Srv)
	return err
}

func (c *Common) start(ctx context.Context, desc *grpc.ServiceDesc, srv interface{}) (Plugin, error) {
	//Start the grpc server on unix socket
	uuid := sdk.UUID()
	c.Socket = filepath.Join(os.TempDir(), fmt.Sprintf("grpcplugin-socket-%s.sock", uuid))
	syscall.Unlink(c.Socket)
	l, err := net.Listen("unix", c.Socket)
	if err != nil {
		return nil, fmt.Errorf("unable to listen on socket %s: %v", c.Socket, err)
	}

	s := grpc.NewServer()
	c.s = s
	c.s.RegisterService(desc, srv)
	reflection.Register(s)

	go func() {
		<-ctx.Done()
		fmt.Printf("exiting plugin")
		defer os.RemoveAll(c.Socket)
		c.s.Stop()
	}()

	go func() {
		time.Sleep(5 * time.Millisecond)
		socket, _ := filepath.Abs(c.Socket)
		fmt.Printf("%s is ready to accept new connection\n", socket)
	}()

	return c, s.Serve(l)
}

func (c *Common) Stop() {
	c.s.Stop()
	return
}

// InfoMarkdown returns string formatted with markdown
func InfoMarkdown(pl sdk.GRPCPlugin) string {
	var sp string
	for _, param := range pl.Parameters {
		sp += fmt.Sprintf("* **%s**: %s\n", param.Name, param.Description)
	}

	info := fmt.Sprintf(`
%s

## Parameters

%s

## More

More documentation on [Github](https://github.com/ovh/cds/tree/master/contrib/grpcplugins/action/%s/README.md)

`,
		pl.Description,
		sp,
		strings.Replace(pl.Name, "plugin-", "", 1))

	return info
}
