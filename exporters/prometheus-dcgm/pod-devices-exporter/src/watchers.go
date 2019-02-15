// Copyright (c) 2018, NVIDIA CORPORATION. All rights reserved.

package main

import (
	"fmt"
	"os"
	"os/signal"

	"gopkg.in/fsnotify/fsnotify.v1"
)

func watchDir(path string) (*fsnotify.Watcher, error) {
	// Make sure the arg is a dir
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("error getting information for %s: %v", path, err)
	}

	if !fi.Mode().IsDir() {
		return nil, fmt.Errorf("%s is not a directory", path)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create FS Watcher: %v", err)
	}

	err = watcher.Add(path)
	if err != nil {
		watcher.Close()
		return nil, fmt.Errorf("failed to add %s to Watcher: %v", path, err)
	}
	return watcher, nil
}

func sigWatcher(sigs ...os.Signal) chan os.Signal {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, sigs...)
	return sigChan
}
