package main

import (
	"bufio"
	"io/ioutil"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

func runScript(path string) (err error) {
	var count int64
	defer func() {
		dbg.Printf("script `%s` exited; emitted %d stat(s)", path, count)
	}()
	cmd := exec.Command(path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		if e2 := cmd.Wait(); e2 != nil && err == nil {
			err = e2
		}
	}()
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Bytes()
		handleMessage(line)
		count++
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func runScripts() {
	var scriptMutex sync.Mutex // protects currentlyRunning
	currentlyRunning := make(map[string]struct{})
	ticker := time.NewTicker(time.Duration(conf.Scripts.RunIntervalMS) * time.Millisecond)
	for _ = range ticker.C {
		files, err := ioutil.ReadDir(conf.Scripts.Path)
		if err != nil {
			dbg.Printf("failed to read scripts in %s: %s", conf.Scripts.Path, err)
			metaCount("run_scripts_list_dir_failures")
			continue
		}
		scriptMutex.Lock()
		for _, file := range files {
			if !file.Mode().IsRegular() {
				continue
			}
			path := filepath.Join(conf.Scripts.Path, file.Name())
			if _, ok := currentlyRunning[path]; ok {
				dbg.Printf("not running script because a previous instance is still running: %s", path)
				continue
			}
			dbg.Printf("running script: %s", path)
			currentlyRunning[path] = struct{}{}
			go func(p string) {
				if err := runScript(p); err != nil {
					dbg.Printf("error running script at %s: %s", p, err)
					metaCount("run_script_failures")
				}
				scriptMutex.Lock()
				delete(currentlyRunning, path)
				scriptMutex.Unlock()
			}(path)
		}
		scriptMutex.Unlock()
	}
}
