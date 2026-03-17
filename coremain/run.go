/*
 * Copyright (C) 2020-2022, IrineSistiana
 *
 * This file is part of mosdns.
 *
 * mosdns is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * mosdns is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package coremain

import (
	"fmt"
	"github.com/IrineSistiana/mosdns/v5/mlog"
	"github.com/kardianos/service"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// <<< ADDED: Global variable to store the base directory for other packages to use.
var MainConfigBaseDir string

type serverFlags struct {
	c         string
	dir       string
	cpu       int
	asService bool
}

var rootCmd = &cobra.Command{
	Use: "mosdns",
}

func init() {
	sf := new(serverFlags)
	startCmd := &cobra.Command{
		Use:   "start [-c config_file] [-d working_dir]",
		Short: "Start mosdns main program.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if sf.asService {
				svc, err := service.New(&serverService{f: sf}, svcCfg)
				if err != nil {
					return fmt.Errorf("failed to init service, %w", err)
				}
				return svc.Run()
			}

			m, err := NewServer(sf)
			if err != nil {
				return err
			}

			go func() {
				c := make(chan os.Signal, 1)
				signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
				sig := <-c
				m.logger.Warn("signal received", zap.Stringer("signal", sig))
				m.sc.SendCloseSignal(nil)
			}()
			return m.GetSafeClose().WaitClosed()
		},
		DisableFlagsInUseLine: true,
		SilenceUsage:          true,
	}
	rootCmd.AddCommand(startCmd)
	fs := startCmd.Flags()
	fs.StringVarP(&sf.c, "config", "c", "", "config file")
	fs.StringVarP(&sf.dir, "dir", "d", "", "working dir")
	fs.IntVar(&sf.cpu, "cpu", 0, "set runtime.GOMAXPROCS")
	fs.BoolVar(&sf.asService, "as-service", false, "start as a service")
	_ = fs.MarkHidden("as-service")

	serviceCmd := &cobra.Command{
		Use:   "service",
		Short: "Manage mosdns as a system service.",
	}
	serviceCmd.PersistentPreRunE = initService
	serviceCmd.AddCommand(
		newSvcInstallCmd(),
		newSvcUninstallCmd(),
		newSvcStartCmd(),
		newSvcStopCmd(),
		newSvcRestartCmd(),
		newSvcStatusCmd(),
	)
	rootCmd.AddCommand(serviceCmd)
	rootCmd.AddCommand(newConfigCmd())
	rootCmd.AddCommand(newControlCmd())
}

func AddSubCmd(c *cobra.Command) {
	rootCmd.AddCommand(c)
}

func Run() error {
	return rootCmd.Execute()
}

func NewServer(sf *serverFlags) (*Mosdns, error) {
	if sf.cpu > 0 {
		runtime.GOMAXPROCS(sf.cpu)
	}

	if len(sf.dir) > 0 {
		err := os.Chdir(sf.dir)
		if err != nil {
			return nil, fmt.Errorf("failed to change the current working directory, %w", err)
		}
		mlog.L().Info("working directory changed", zap.String("path", sf.dir))
	}

	cfg, fileUsed, err := loadConfig(sf.c)
	if err != nil {
		return nil, fmt.Errorf("fail to load config, %w", err)
	}

	// <<< ADDED: Determine and set the main config base directory.
	// This ensures the path is absolute and available for other packages.
	if fileUsed != "" {
		if absPath, err := filepath.Abs(fileUsed); err == nil {
			MainConfigBaseDir = filepath.Dir(absPath)
		} else {
			MainConfigBaseDir = filepath.Dir(fileUsed)
		}
	} else if len(sf.dir) > 0 {
		if absPath, err := filepath.Abs(sf.dir); err == nil {
			MainConfigBaseDir = absPath
		} else {
			MainConfigBaseDir = sf.dir
		}
	} else {
		if wd, err := os.Getwd(); err == nil {
			MainConfigBaseDir = wd
		}
	}
	mlog.L().Info("main config base directory set", zap.String("path", MainConfigBaseDir))
	setRuntimeStateDBPath(cfg.ControlDBPath)

	// <<< ADDED: Explicitly initialize the audit collector with the correct base path.
	InitializeAuditCollector(MainConfigBaseDir, cfg.Audit)
	// <<< END ADDED SECTION

	mlog.L().Info("main config loaded", zap.String("file", fileUsed))

	return NewMosdns(cfg)
}

// loadConfig loads a v2 config from a file. If filePath is empty, it will
// automatically search and load a file which name start with "config".
func loadConfig(filePath string) (*Config, string, error) {
	_, raw, fileUsed, err := resolveConfigInput(filePath)
	if err != nil {
		return nil, "", err
	}

	isV2, err := isConfigV2Document(raw)
	if err != nil {
		return nil, "", err
	}
	if !isV2 {
		return nil, "", fmt.Errorf("only config v2 is supported: %s", fileUsed)
	}
	cfg, err := compileConfigV2(raw)
	if err != nil {
		return nil, "", fmt.Errorf("failed to compile config v2: %w", err)
	}

	cfg.baseDir = resolveBaseDir(fileUsed)
	cfg.ControlDBPath = resolveRuntimeStateDBPathForConfig(cfg.baseDir, cfg.ControlDBPath)
	return cfg, fileUsed, nil
}

func resolveBaseDir(fileUsed string) string {
	if len(fileUsed) > 0 {
		if abs, err := filepath.Abs(fileUsed); err == nil {
			return filepath.Dir(abs)
		}
		return filepath.Dir(fileUsed)
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return ""
}

func resolveRuntimeStateDBPathForConfig(baseDir, configured string) string {
	if strings.TrimSpace(configured) == "" {
		return filepath.Join(baseDir, runtimeStateDBFilename)
	}
	if filepath.IsAbs(configured) {
		return configured
	}
	return filepath.Join(baseDir, configured)
}
