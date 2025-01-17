/*
Copyright 2016 The Kubernetes Authors All rights reserved.

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

package constants

import (
	"os"
	"path/filepath"
)

// MachineName is the name to use for the VM.
const MachineName = "minikubeVM"

// Fix for windows
var Minipath = filepath.Join(os.Getenv("HOME"), ".minikube")

// TODO: Fix for windows
// KubeconfigPath is the path to the Kubernetes client config
var KubeconfigPath = filepath.Join(os.Getenv("HOME"), ".kube", "config")

// MinikubeContext is the kubeconfig context name used for minikube
const MinikubeContext = "minikube"

// MakeMiniPath is a utility to calculate a relative path to our directory.
func MakeMiniPath(fileName string) string {
	return filepath.Join(Minipath, fileName)
}
