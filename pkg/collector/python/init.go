// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-2019 Datadog, Inc.

// +build python

package python

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/DataDog/datadog-agent/pkg/aggregator"
	"github.com/DataDog/datadog-agent/pkg/config"
	"github.com/DataDog/datadog-agent/pkg/metrics"
	"github.com/DataDog/datadog-agent/pkg/util/cache"
	"github.com/DataDog/datadog-agent/pkg/util/executable"
	"github.com/DataDog/datadog-agent/pkg/version"
)

/*
#include <datadog_agent_six.h>
#cgo !windows LDFLAGS: -ldatadog-agent-six -ldl
#cgo windows LDFLAGS: -ldatadog-agent-six -lstdc++ -static

#include <stdlib.h>

// helpers

char *getStringAddr(char **array, unsigned int idx) {
	return array[idx];
}

//
// init free method
//
// On windows we need to free memory in the same DLL where it was allocated.
// This allows six to free memory returned by Go callbacks.
//

void initCgoFree(six_t *six) {
	set_cgo_free_cb(six, free);
}

//
// datadog_agent module
//
// This also init "util" module who expose the same "headers" function
//

void GetVersion(char **);
void GetHostname(char **);
void GetClusterName(char **);
void Headers(char **);
void GetConfig(char*, char **);
void LogMessage(char *, int);
void SetExternalTags(char *, char *, char **);

void initDatadogAgentModule(six_t *six) {
	set_get_version_cb(six, GetVersion);
	set_get_hostname_cb(six, GetHostname);
	set_get_clustername_cb(six, GetClusterName);
	set_headers_cb(six, Headers);
	set_log_cb(six, LogMessage);
	set_get_config_cb(six, GetConfig);
	set_set_external_tags_cb(six, SetExternalTags);
}

//
// aggregator module
//

void SubmitMetric(char *, metric_type_t, char *, float, char **, int, char *);
void SubmitServiceCheck(char *, char *, int, char **, int, char *, char *);
void SubmitEvent(char *, event_t *, int);

void initAggregatorModule(six_t *six) {
	set_submit_metric_cb(six, SubmitMetric);
	set_submit_service_check_cb(six, SubmitServiceCheck);
	set_submit_event_cb(six, SubmitEvent);
}

//
// _util module
//

void GetSubprocessOutput(char **, int, char **, char **, int*, char **);

void initUtilModule(six_t *six) {
	set_get_subprocess_output_cb(six, GetSubprocessOutput);
}

//
// tagger module
//

char **Tags(char **, int);

void initTaggerModule(six_t *six) {
	set_tags_cb(six, Tags);
}

//
// containers module
//

int IsContainerExcluded(char *, char *);

void initContainersModule(six_t *six) {
	set_is_excluded_cb(six, IsContainerExcluded);
}

//
// kubeutil module
//

void GetKubeletConnectionInfo(char *);

void initkubeutilModule(six_t *six) {
	set_get_connection_info_cb(six, GetKubeletConnectionInfo);
}
*/
import "C"

var (
	// PythonVersion contains the interpreter version string provided by
	// `sys.version`. It's empty if the interpreter was not initialized.
	PythonVersion = ""
	// The pythonHome variable typically comes from -ldflags
	// it's needed in case the agent was built using embedded libs
	pythonHome2 = ""
	pythonHome3 = ""
	// PythonHome contains the computed value of the Python Home path once the
	// intepreter is created. It might be empty in case the interpreter wasn't
	// initialized, or the Agent was built using system libs and the env var
	// PYTHONHOME is empty. It's expected to always contain a value when the
	// Agent is built using embedded libs.
	PythonHome = ""
	// PythonPath contains the string representation of the Python list returned
	// by `sys.path`. It's empty if the interpreter was not initialized.
	PythonPath = ""

	six *C.six_t = nil
)

func sendTelemetry(pythonVersion int) {
	tags := []string{
		fmt.Sprintf("python_version:%d", pythonVersion),
	}
	if agentVersion, err := version.New(version.AgentVersion, version.Commit); err == nil {
		tags = append(tags,
			fmt.Sprintf("agent_version_major:%d", agentVersion.Major),
			fmt.Sprintf("agent_version_minor:%d", agentVersion.Minor),
			fmt.Sprintf("agent_version_patch:%d", agentVersion.Patch),
		)
	}
	aggregator.AddRecurrentSeries(&metrics.Serie{
		Name:   "datadog.agent.python.version",
		Points: []metrics.Point{{Value: 1.0}},
		Tags:   tags,
		MType:  metrics.APIGaugeType,
	})
}

func Initialize(paths ...string) error {
	pythonVersion := config.Datadog.GetInt("python_version")

	if pythonVersion == 2 {
		six = C.make2(C.CString(pythonHome2))
		PythonHome = pythonHome2
	} else if pythonVersion == 3 {
		six = C.make3(C.CString(pythonHome3))
		PythonHome = pythonHome3
	} else {
		return fmt.Errorf("unknown requested version of python: %d", pythonVersion)
	}

	if six == nil {
		return fmt.Errorf("could not init six lib for python version %d", pythonVersion)
	}

	if runtime.GOOS == "windows" {
		_here, _ := executable.Folder()
		// on windows, override the hardcoded path set during compile time, but only if that path points to nowhere
		if _, err := os.Stat(filepath.Join(PythonHome, "lib", "python2.7")); os.IsNotExist(err) {
			PythonHome = _here
		}
	}

	// Set the PYTHONPATH if needed.
	for _, p := range paths {
		C.add_python_path(six, C.CString(p))
	}

	C.init(six)

	if C.is_initialized(six) == 0 {
		err := C.GoString(C.get_error(six))
		return fmt.Errorf("%s", err)
	}

	// store the Python version after killing \n chars within the string
	if res := C.get_py_version(six); res != nil {
		PythonVersion = strings.Replace(C.GoString(res), "\n", "", -1)

		// Set python version in the cache
		key := cache.BuildAgentKey("pythonVersion")
		cache.Cache.Set(key, PythonVersion, cache.NoExpiration)
	}

	sendTelemetry(pythonVersion)

	// TODO: query PythonPath
	// TODO: query PythonHome

	C.initCgoFree(six)
	C.initDatadogAgentModule(six)
	C.initAggregatorModule(six)
	C.initUtilModule(six)
	C.initTaggerModule(six)
	initContainerFilter() // special init for the container go code
	C.initContainersModule(six)
	C.initkubeutilModule(six)

	return nil
}

// Destroy destroys the loaded Python interpreter initialized by 'Initialize'
func Destroy() {
	if six != nil {
		C.destroy(six)
	}
}

// GetSix returns the underlying six_t struct. This is meant for testing and
// tooling, use the six_t struct at your own risk
func GetSix() *C.six_t {
	return six
}
