//go:build !windows

/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2017-2025 WireGuard LLC. All Rights Reserved.
 */

package main

import (
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strconv"

	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/ipc"
	"golang.zx2c4.com/wireguard/tun"
)

const (
	ExitSetupSuccess = 0
	ExitSetupFailed  = 1
)

const (
	ENV_WG_TUN_FD             = "WG_TUN_FD"
	ENV_WG_UAPI_FD            = "WG_UAPI_FD"
	ENV_WG_PROCESS_FOREGROUND = "WG_PROCESS_FOREGROUND"
)

func printUsage() {
	fmt.Printf("Usage: %s [-f/--foreground] INTERFACE-NAME\n", os.Args[0])
}

func warning() {
	switch runtime.GOOS {
	case "linux", "freebsd", "openbsd":
		if os.Getenv(ENV_WG_PROCESS_FOREGROUND) == "1" {
			return
		}
	default:
		return
	}

	fmt.Fprintln(os.Stderr, "┌──────────────────────────────────────────────────────┐")
	fmt.Fprintln(os.Stderr, "│                                                      │")
	fmt.Fprintln(os.Stderr, "│   Running wireguard-go is not required because this  │")
	fmt.Fprintln(os.Stderr, "│   kernel has first class support for WireGuard. For  │")
	fmt.Fprintln(os.Stderr, "│   information on installing the kernel module,       │")
	fmt.Fprintln(os.Stderr, "│   please visit:                                      │")
	fmt.Fprintln(os.Stderr, "│         https://www.wireguard.com/install/           │")
	fmt.Fprintln(os.Stderr, "│                                                      │")
	fmt.Fprintln(os.Stderr, "└──────────────────────────────────────────────────────┘")
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Printf("wireguard-go v%s\n\nUserspace WireGuard daemon for %s-%s.\nInformation available at https://www.wireguard.com.\nCopyright (C) Jason A. Donenfeld <Jason@zx2c4.com>.\n", Version, runtime.GOOS, runtime.GOARCH)
		return
	}

	warning()

	var foreground bool
	var interfaceName string
	if len(os.Args) < 2 || len(os.Args) > 3 { // 2/3 个参数
		printUsage()
		return
	}

	switch os.Args[1] {

	case "-f", "--foreground":
		foreground = true
		if len(os.Args) != 3 {
			printUsage()
			return
		}
		interfaceName = os.Args[2]

	default:
		foreground = false
		if len(os.Args) != 2 {
			printUsage()
			return
		}
		interfaceName = os.Args[1]
	}

	// 如果参数没有指定 -f, 那么还要检查环境变量
	if !foreground {
		foreground = os.Getenv(ENV_WG_PROCESS_FOREGROUND) == "1"
	}

	// get log level (default: info)

	logLevel := func() int {
		switch os.Getenv("LOG_LEVEL") {
		case "verbose", "debug":
			return device.LogLevelVerbose
		case "error":
			return device.LogLevelError
		case "silent":
			return device.LogLevelSilent
		}
		return device.LogLevelError
	}()

	// open TUN device (or use supplied fd)

	tdev, err := func() (tun.Device, error) { // 声明了一个匿名函数, 并立即调用, 好处是: 封装逻辑, 避免变量污染
		tunFdStr := os.Getenv(ENV_WG_TUN_FD)
		if tunFdStr == "" {
			return tun.CreateTUN(interfaceName, device.DefaultMTU)
		}

		// construct tun device from supplied fd

		fd, err := strconv.ParseUint(tunFdStr, 10, 32) // 字符串转成 uint64
		if err != nil {
			return nil, err
		}

		err = unix.SetNonblock(int(fd), true) // 对网络设备来说, 非阻塞是标准做法
		if err != nil {
			return nil, err
		}

		file := os.NewFile(uintptr(fd), "") // 将 fd 转换成 go 的 *os.file
		return tun.CreateTUNFromFile(file, device.DefaultMTU)
	}()

	if err == nil { // 没发生错误
		realInterfaceName, err2 := tdev.Name() // 可能系统会对设备进行重命名
		if err2 == nil {
			interfaceName = realInterfaceName
		}
	}

	logger := device.NewLogger(
		logLevel,
		fmt.Sprintf("(%s) ", interfaceName),
	)

	logger.Verbosef("Starting wireguard-go version %s", Version)

	if err != nil {
		logger.Errorf("Failed to create TUN device: %v", err)
		os.Exit(ExitSetupFailed)
	}

	// open UAPI file (or use supplied fd)

	fileUAPI, err := func() (*os.File, error) {
		uapiFdStr := os.Getenv(ENV_WG_UAPI_FD)
		if uapiFdStr == "" {
			return ipc.UAPIOpen(interfaceName)
		}

		// use supplied fd

		fd, err := strconv.ParseUint(uapiFdStr, 10, 32)
		if err != nil {
			return nil, err
		}

		return os.NewFile(uintptr(fd), ""), nil
	}()
	if err != nil {
		logger.Errorf("UAPI listen error: %v", err)
		os.Exit(ExitSetupFailed)
		return
	}
	// daemonize the process

	if !foreground {
		env := os.Environ()
		env = append(env, fmt.Sprintf("%s=3", ENV_WG_TUN_FD))
		env = append(env, fmt.Sprintf("%s=4", ENV_WG_UAPI_FD))
		env = append(env, fmt.Sprintf("%s=1", ENV_WG_PROCESS_FOREGROUND)) // daemon 进程以 foreground 的方式运行, 这就可以让重新启动的进程不会进入到这个分支里面
		files := [3]*os.File{}                                            // 声明容量为 3, 元素为 *os.File 的数组
		if os.Getenv("LOG_LEVEL") != "" && logLevel != device.LogLevelSilent {
			files[0], _ = os.Open(os.DevNull)
			files[1] = os.Stdout
			files[2] = os.Stderr
		} else {
			files[0], _ = os.Open(os.DevNull)
			files[1], _ = os.Open(os.DevNull)
			files[2], _ = os.Open(os.DevNull)
		}
		attr := &os.ProcAttr{
			Files: []*os.File{
				files[0],    // stdin
				files[1],    // stdout
				files[2],    // stderr
				tdev.File(), // 文件描述符 3
				fileUAPI,    // uapi 接口文件
			},
			Dir: ".",
			Env: env,
		}

		path, err := os.Executable() // 用户获取当前运行的可执行文件的完整路径
		if err != nil {
			logger.Errorf("Failed to determine executable: %v", err)
			os.Exit(ExitSetupFailed)
		}

		process, err := os.StartProcess( // 当程序以非前台
			path,
			os.Args,
			attr,
		)
		if err != nil {
			logger.Errorf("Failed to daemonize: %v", err)
			os.Exit(ExitSetupFailed)
		}
		process.Release() // 用来释放父进程对子进程的引用, 父进程让子进程挂到守护进程下
		return
	}

	// 能走到下面, 有两种情况: 1. 守护进程; 2. 一开始就让他以前台方式运行

	device := device.NewDevice(tdev, conn.NewDefaultBind(), logger)

	logger.Verbosef("Device started")

	errs := make(chan error) // channel
	term := make(chan os.Signal, 1)

	// 这个 UAPI: 提供一个接口让用户可以配置 wireguard, 比方说添加 peer, 设置密钥等
	// 通常是通过 wg 命令行工具来操作
	uapi, err := ipc.UAPIListen(interfaceName, fileUAPI)
	if err != nil {
		logger.Errorf("Failed to listen on uapi socket: %v", err)
		os.Exit(ExitSetupFailed)
	}

	go func() {
		for {
			conn, err := uapi.Accept() // 开一个线程, 监听是否有用户配置
			if err != nil {
				errs <- err
				return
			}
			go device.IpcHandle(conn)
		}
	}()

	logger.Verbosef("UAPI listener started")

	// wait for program to terminate

	signal.Notify(term, unix.SIGTERM)
	signal.Notify(term, os.Interrupt)

	// 这个是 io 复用, 阻塞的
	select {
	case <-term:
	case <-errs:
	case <-device.Wait():
	}

	// clean up

	uapi.Close()
	device.Close()

	logger.Verbosef("Shutting down")
}
