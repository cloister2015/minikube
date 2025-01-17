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

package cmd

import (
	"net"

	flag "github.com/spf13/pflag"

	"k8s.io/minikube/pkg/localkube"
	"k8s.io/minikube/pkg/util"
)

func NewLocalkubeServer() *localkube.LocalkubeServer {
	// net.ParseCIDR returns multiple values. Use the IPNet return value
	_, defaultServiceClusterIPRange, _ := net.ParseCIDR("10.0.0.1/24")

	return &localkube.LocalkubeServer{
		Containerized:            false,
		EnableDNS:                true,
		DNSDomain:                util.DNSDomain,
		DNSIP:                    net.ParseIP("10.0.0.10"),
		LocalkubeDirectory:       util.LocalkubeDirectory,
		ServiceClusterIPRange:    *defaultServiceClusterIPRange,
		APIServerAddress:         net.ParseIP("0.0.0.0"),
		APIServerPort:            443,
		APIServerInsecureAddress: net.ParseIP("127.0.0.1"),
		APIServerInsecurePort:    8080,
		ShouldGenerateCerts:      true,
	}
}

// AddFlags adds flags for a specific LocalkubeServer
func AddFlags(s *localkube.LocalkubeServer) {
	flag.BoolVar(&s.Containerized, "containerized", s.Containerized, "If kubelet should run in containerized mode")
	flag.BoolVar(&s.EnableDNS, "enable-dns", s.EnableDNS, "If dns should be enabled")
	flag.StringVar(&s.DNSDomain, "dns-domain", s.DNSDomain, "The cluster dns domain")
	flag.IPVar(&s.DNSIP, "dns-ip", s.DNSIP, "The cluster dns IP")
	flag.StringVar(&s.LocalkubeDirectory, "localkube-directory", s.LocalkubeDirectory, "The directory localkube will store files in")
	flag.IPNetVar(&s.ServiceClusterIPRange, "service-cluster-ip-range", s.ServiceClusterIPRange, "The service-cluster-ip-range for the apiserver")
	flag.IPVar(&s.APIServerAddress, "apiserver-address", s.APIServerAddress, "The address the apiserver will listen securely on")
	flag.IntVar(&s.APIServerPort, "apiserver-port", s.APIServerPort, "The port the apiserver will listen securely on")
	flag.IPVar(&s.APIServerInsecureAddress, "apiserver-insecure-address", s.APIServerInsecureAddress, "The address the apiserver will listen insecurely on")
	flag.IntVar(&s.APIServerInsecurePort, "apiserver-insecure-port", s.APIServerInsecurePort, "The port the apiserver will listen insecurely on")
	flag.BoolVar(&s.ShouldGenerateCerts, "generate-certs", s.ShouldGenerateCerts, "If localkube should generate it's own certificates")

	// These two come from vendor/ packages that use flags. We should hide them
	flag.CommandLine.MarkHidden("google-json-key")
	flag.CommandLine.MarkHidden("log-flush-frequency")

	// Parse them
	flag.Parse()
}
