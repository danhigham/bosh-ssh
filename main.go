// bosh-ssh -e env_name -d deploy_name job_name job_name

package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	cmdconf "github.com/cloudfoundry/bosh-cli/cmd/config"
	boshdir "github.com/cloudfoundry/bosh-cli/director"
	boshuaa "github.com/cloudfoundry/bosh-cli/uaa"
	boshlog "github.com/cloudfoundry/bosh-utils/logger"
	"github.com/kr/pty"
	"golang.org/x/crypto/ssh/terminal"
)

var (
	uaaURL      = os.Getenv("UAA_URL")      // eg "https://some-uaa:8443"
	directorURL = os.Getenv("DIRECTOR_URL") // eg "https://some-director"

	uaaClient       = os.Getenv("BOSH_CLIENT")        // eg "my-script"
	uaaClientSecret = os.Getenv("BOSH_CLIENT_SECRET") // eg "my-script-secret"

	someCA = os.Getenv("BOSH_CA_CERT")
)

type Layout struct {
	Width  int
	Height int
}

func main() {

	deploymentName := flag.String("d", "", "Deployment Name")
	flag.Parse()

	uaa, err := buildUAA()
	if err != nil {
		panic(err)
	}

	director, err := buildDirector(uaa)
	if err != nil {
		panic(err)
	}

	// Fetch information about the Director.
	info, err := director.Info()
	if err != nil {
		panic(err)
	}

	fmt.Printf("Director: %s\n", info.Name)

	// See director/interfaces.go for a full list of methods.
	dep, err := director.FindDeployment(*deploymentName)
	if err != nil {
		panic(err)
	}

	instances, err := dep.Instances()
	if err != nil {
		panic(err)
	}

	jobsNames := []string{}

	for _, instance := range instances {
		if matchesJobNames(fmt.Sprintf("%s/%s", instance.Group, instance.ID)) {
			jobsNames = append(jobsNames, fmt.Sprintf("%s/%s", instance.Group, instance.ID))
		}
	}

	boshSSHCmd := fmt.Sprintf("bosh -d %s ssh %s", *deploymentName, jobsNames[0])
	cmd := exec.Command("tmux", "new-session", "-s", "bosh-ssh", "-d", boshSSHCmd)
	err = cmd.Run()
	if err != nil {
		panic(err)
	}

	fmt.Printf("%+v\n", jobsNames)

	layouts := make(map[int]Layout)
	layouts[1] = Layout{Height: 1, Width: 1}
	layouts[2] = Layout{Height: 2, Width: 1}
	layouts[3] = Layout{Height: 2, Width: 2}
	layouts[4] = Layout{Height: 2, Width: 2}
	layouts[5] = Layout{Height: 2, Width: 3}
	layouts[6] = Layout{Height: 2, Width: 3}
	layouts[7] = Layout{Height: 3, Width: 3}
	layouts[8] = Layout{Height: 3, Width: 3}
	layouts[9] = Layout{Height: 3, Width: 3}
	layouts[10] = Layout{Height: 4, Width: 3}

	totalPanes := 1
	jobCount := len(jobsNames)
	layout := layouts[jobCount]

	fmt.Printf("Found %d jobs\n", jobCount)

	for y := 1; y < layout.Height; y++ {
		boshSSHCmd = fmt.Sprintf("bosh -d %s ssh %s", *deploymentName, jobsNames[totalPanes])
		fmt.Println(boshSSHCmd)

		err = exec.Command("tmux", "split-window", "-v", boshSSHCmd).Run()
		if err != nil {
			panic(err)
		}
		totalPanes++
		if totalPanes >= jobCount {
			break
		}
	}

	exec.Command("tmux", "select-layout", "even-vertical").Run()
	// currentRow := 0

	x := 0
	y := 0
	for totalPanes < jobCount {
		fmt.Printf("X: %d Y: %d\n", x, y)
		fmt.Printf("Selecting pane %s\n", strconv.Itoa(y*layout.Width))
		fmt.Printf("Panes so far: %d\n", totalPanes)

		boshSSHCmd = fmt.Sprintf("bosh -d %s ssh %s", *deploymentName, jobsNames[totalPanes])
		fmt.Println(boshSSHCmd)
		err = exec.Command("tmux", "split-window", "-t", strconv.Itoa(y), "-h", boshSSHCmd).Run()
		if err != nil {
			panic(err)
		}
		totalPanes++
		if totalPanes >= jobCount {
			break
		}

		x++
		if x >= layout.Width-1 {
			x = 0
			y++
		}
		if y >= layout.Height {
			break
		}

	}

	err = exec.Command("tmux", "setw", "synchronize-panes").Run()
	if err != nil {
		panic(err)
	}

	if err := attachToTmuxSession(); err != nil {
		panic(err)
	}

}

func attachToTmuxSession() error {
	// Create arbitrary command.
	cmd := exec.Command("tmux", "-2", "attach-session", "-d")

	// Start the command with a pty.
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return err
	}
	// Make sure to close the pty at the end.
	defer func() { _ = ptmx.Close() }() // Best effort.

	// Handle pty size.
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			if err := pty.InheritSize(os.Stdin, ptmx); err != nil {
				log.Printf("error resizing pty: %s", err)
			}
		}
	}()
	ch <- syscall.SIGWINCH // Initial resize.

	// Set stdin in raw mode.
	oldState, err := terminal.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		panic(err)
	}
	defer func() { _ = terminal.Restore(int(os.Stdin.Fd()), oldState) }() // Best effort.

	// Copy stdin to the pty and the pty to stdout.
	go func() { _, _ = io.Copy(ptmx, os.Stdin) }()
	_, _ = io.Copy(os.Stdout, ptmx)

	return nil
}

func matchesJobNames(jobName string) bool {
	argsWithoutProg := os.Args[3:]
	for _, arg := range argsWithoutProg {
		if strings.HasPrefix(jobName, arg) {
			return true
		}
	}

	return false
}

func buildUAA() (boshuaa.UAA, error) {
	logger := boshlog.NewLogger(boshlog.LevelError)
	factory := boshuaa.NewFactory(logger)

	// Build a UAA config from a URL.
	// HTTPS is required and certificates are always verified.
	config, err := boshuaa.NewConfigFromURL(uaaURL)
	if err != nil {
		return nil, err
	}

	// Set client credentials for authentication.
	// Machine level access should typically use a client instead of a particular user.

	config.Client = uaaClient
	config.ClientSecret = uaaClientSecret

	// Configure trusted CA certificates.
	// If nothing is provided default system certificates are used.
	config.CACert = someCA

	return factory.New(config)
}

func buildDirector(uaa boshuaa.UAA) (boshdir.Director, error) {
	logger := boshlog.NewLogger(boshlog.LevelError)
	factory := boshdir.NewFactory(logger)

	// Build a Director config from address-like string.
	// HTTPS is required and certificates are always verified.
	factoryConfig, err := boshdir.NewConfigFromURL(directorURL)
	if err != nil {
		return nil, err
	}

	// Configure custom trusted CA certificates.
	// If nothing is provided default system certificates are used.
	factoryConfig.CACert = someCA

	// Allow Director to fetch UAA tokens when necessary.
	factoryConfig.TokenFunc = boshuaa.NewClientTokenSession(uaa).TokenFunc
	config := cmdconf.FSConfig{}

	return factory.New(factoryConfig, config, boshdir.NewNoopTaskReporter(), boshdir.NewNoopFileReporter())
}
