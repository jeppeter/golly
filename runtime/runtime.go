// Public Domain (-) 2010-2013 The Golly Authors.
// See the Golly UNLICENSE file for details.

// Package runtime package provides utilities to manage the runtime environment
// for processes.
package runtime

import (
	"fmt"
	"github.com/tav/golly/command"
	"github.com/tav/golly/log"
	"github.com/tav/golly/optparse"
	"net"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

const Platform = runtime.GOOS

var (
	Profile  string
	CPUCount int
)

var SignalHandlers = make(map[os.Signal]func())

func handleSignals() {
	notifier := make(chan os.Signal, 100)
	signal.Notify(notifier)
	var sig os.Signal
	for {
		sig = <-notifier
		handler, found := SignalHandlers[sig]
		if found {
			handler()
		}
	}
}

var exitHandlers = []func(){}

func RunExitHandlers() {
	for _, handler := range exitHandlers {
		handler()
	}
}

func RegisterExitHandler(handler func()) {
	exitHandlers = append(exitHandlers, handler)
}

func Exit(code int) {
	log.Wait()
	RunExitHandlers()
	os.Exit(code)
}

func Error(format string, v ...interface{}) {
	log.Error(format, v...)
	Exit(1)
}

func StandardError(err error) {
	log.StandardError(err)
	Exit(1)
}

func CreatePidFile(path string) {
	pidFile, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		StandardError(err)
	}
	fmt.Fprintf(pidFile, "%d", os.Getpid())
	err = pidFile.Close()
	if err != nil {
		StandardError(err)
	}
}

type Lock struct {
	link     string
	file     string
	acquired bool
}

func GetLock(directory string, name string) (lock *Lock, err error) {
	file := path.Join(directory, fmt.Sprintf("%s-%d.lock", name, os.Getpid()))
	lockFile, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		return
	}
	lockFile.Close()
	link := path.Join(directory, name+".lock")
	err = os.Link(file, link)
	if err == nil {
		lock = &Lock{
			link: link,
			file: file,
		}
		RegisterExitHandler(func() { lock.ReleaseLock() })
	} else {
		os.Remove(file)
	}
	return
}

func (lock *Lock) ReleaseLock() {
	os.Remove(lock.file)
	os.Remove(lock.link)
}

// JoinPath joins the given path with the directory unless it happens to be an
// absolute path, in which case it returns the path exactly as it was given.
func JoinPath(directory, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(directory, filepath.Clean(path))
}

// Initwill set Go's internal GOMAXPROCS to double the number of CPUs detected.
func Init() {
	runtime.GOMAXPROCS(CPUCount * 2)
}

// InitProcess acquires a process lock and writes the PID file for the current
// process.
func InitProcess(name, runPath string) {

	// Get the runtime lock to ensure we only have one process of any given name
	// running within the same run path at any time.
	_, err := GetLock(runPath, name)
	if err != nil {
		Error("Couldn't successfully acquire a process lock:\n\n\t%s\n", err)
	}

	// Write the process ID into a file for use by external scripts.
	go CreatePidFile(filepath.Join(runPath, name+".pid"))

}

// DefaultOpts processes default runtime command line options.
func DefaultOpts(name string, opts *optparse.Parser, argv []string, autoExit bool) (bool, string, string, string, bool) {

	var (
		configPath        string
		instanceDirectory string
		err               error
	)

	debug := opts.Bool([]string{"-d", "--debug"},
		"enable debug mode")

	genConfig := opts.Bool([]string{"-g", "--gen-config"},
		"show the default yaml config")

	runDirectory := opts.StringConfig("run-dir", "run",
		"the path to the run directory to store locks, pid files, etc. [run]")

	logDirectory := opts.StringConfig("log-dir", "log",
		"the path to the log directory [log]")

	logRotate := opts.StringConfig("log-rotate", "never",
		"specify one of 'hourly', 'daily' or 'never' [never]")

	noConsoleLog := opts.BoolConfig("no-console-log",
		"disable server requests being logged to the console [false]")

	extraConfig := opts.StringConfig("extra-config", "",
		"path to a YAML config file with additional options")

	// Parse the command line options.
	args := opts.Parse(argv)

	// Print the default YAML config file if the ``-g`` flag was specified.
	if *genConfig {
		opts.PrintDefaultConfigFile(name)
		Exit(0)
	}

	// Enable the console logger early.
	if !*noConsoleLog {
		log.AddConsoleLogger()
	}

	// Assume the parent directory of the config as the instance directory.
	if len(args) >= 1 {
		var statInfo os.FileInfo
		configPath, err = filepath.Abs(filepath.Clean(args[0]))
		if err != nil {
			StandardError(err)
		}
		statInfo, err = os.Stat(configPath)
		if err != nil {
			StandardError(err)
		}
		if statInfo.IsDir() {
			instanceDirectory = configPath
			Profile = "default"
		} else {
			err = opts.ParseConfig(configPath, os.Args)
			if err != nil {
				StandardError(err)
			}
			instanceDirectory, _ = filepath.Split(configPath)
			Profile = strings.Split(filepath.Base(configPath), ".")[0]
		}
	} else {
		if autoExit {
			opts.PrintUsage()
			Exit(0)
		}
		return false, "", "", "", true
	}

	// Load the extra config file with additional options if one has been
	// specified.
	if *extraConfig != "" {
		extraConfigPath, err := filepath.Abs(filepath.Clean(*extraConfig))
		if err != nil {
			StandardError(err)
		}
		extraConfigPath = JoinPath(instanceDirectory, extraConfigPath)
		err = opts.ParseConfig(extraConfigPath, os.Args)
		if err != nil {
			StandardError(err)
		}
	}

	// Create the log directory if it doesn't exist.
	logPath := JoinPath(instanceDirectory, *logDirectory)
	err = os.MkdirAll(logPath, 0755)
	if err != nil {
		StandardError(err)
	}

	// Create the run directory if it doesn't exist.
	runPath := JoinPath(instanceDirectory, *runDirectory)
	err = os.MkdirAll(runPath, 0755)
	if err != nil {
		StandardError(err)
	}

	// Setup the file and console logging.
	var rotate int

	switch *logRotate {
	case "daily":
		rotate = log.RotateDaily
	case "hourly":
		rotate = log.RotateHourly
	case "never":
		rotate = log.RotateNever
	default:
		Error("Unknown log rotation format %q", *logRotate)
	}

	_, err = log.AddFileLogger(name, logPath, rotate, log.InfoLog)
	if err != nil {
		Error("Couldn't initialise logfile: %s", err)
	}

	_, err = log.AddFileLogger("error", logPath, rotate, log.ErrorLog)
	if err != nil {
		Error("Couldn't initialise logfile: %s", err)
	}

	// Initialise the runtime -- which will run the process on multiple
	// processors if possible.
	Init()

	// Initialise the process-related resources.
	if Platform != "windows" {
		InitProcess(name, runPath)
	}

	return *debug, instanceDirectory, runPath, logPath, false

}

// GetCPUCount tries to detect the number of CPUs on the current machine.
func GetCPUCount() (count int) {
	// On BSD systems, it should be possible to use ``sysctl -n hw.ncpu`` to
	// figure this out.
	if (Platform == "darwin") || (Platform == "freebsd") {
		output, err := command.GetOutput(
			[]string{"/usr/sbin/sysctl", "-n", "hw.ncpu"},
		)
		if err != nil {
			return 1
		}
		count, err = strconv.Atoi(strings.TrimSpace(output))
		if err != nil {
			return 1
		}
		// Linux systems provide introspection via ``/proc/cpuinfo``.
	} else if Platform == "linux" {
		output, err := command.GetOutput([]string{"/bin/cat", "/proc/cpuinfo"})
		if err != nil {
			return 1
		}
		for _, line := range strings.Split(output, "\n") {
			if strings.HasPrefix(line, "processor") {
				count += 1
			}
		}
	}
	// For unknown platforms, we assume that there's just a single processor.
	if count == 0 {
		return 1
	}
	return count
}

// GetIP tries to determine the IP address of the current machine.
func GetIP() string {
	hostname, err := os.Hostname()
	if err != nil {
		StandardError(err)
	}
	addrs, err := net.LookupHost(hostname)
	if err != nil {
		StandardError(err)
	}
	var ip string
	for _, addr := range addrs {
		if strings.Contains(addr, ":") || strings.HasPrefix(addr, "127.") {
			continue
		}
		ip = addr
		break
	}
	if ip == "" {
		Error("Couldn't determine local IP address")
	}
	return ip
}

// GetAddr returns host:port and fills in empty host parameter with the current
// machine's IP address if need be.
func GetAddr(host string, port int) string {
	if host == "" {
		host = GetIP()
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// GetAddrListener tries to determine the IP address of the machine when the
// host variable is empty and binds a TCP listener to the given host:port.
func GetAddrListener(host string, port int) (string, net.Listener) {
	addr := GetAddr(host, port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		Error("Cannot listen on %s: %v", addr, err)
	}
	return addr, listener
}

func init() {

	// Set the ``runtime.CPUCount`` variable to the number of CPUs detected.
	CPUCount = GetCPUCount()

	// Register default handlers for SIGINT and SIGTERM.
	SignalHandlers[os.Interrupt] = func() { Exit(0) }
	SignalHandlers[syscall.SIGTERM] = func() { Exit(0) }
	go handleSignals()

}
