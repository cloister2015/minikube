/*
Modified by Redspread to allow external access. Original at:
https://github.com/kubernetes/kubernetes/blob/a3c00aa/cluster/addons/dns/kube2sky/kube2sky.go

Copyright 2014 The Kubernetes Authors All rights reserved.

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

// kube2sky is a bridge between Kubernetes and SkyDNS. It watches the
// Kubernetes master for changes in Services and manifests them into etcd for
// SkyDNS to serve as DNS records.
package kube2sky

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	etcd "github.com/coreos/go-etcd/etcd"
	"github.com/golang/glog"
	skymsg "github.com/skynetservices/skydns/msg"
	kapi "k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/endpoints"
	"k8s.io/kubernetes/pkg/api/unversioned"
	kcache "k8s.io/kubernetes/pkg/client/cache"
	"k8s.io/kubernetes/pkg/client/restclient"
	kclient "k8s.io/kubernetes/pkg/client/unversioned"
	kclientcmd "k8s.io/kubernetes/pkg/client/unversioned/clientcmd"
	kframework "k8s.io/kubernetes/pkg/controller/framework"
	kselector "k8s.io/kubernetes/pkg/fields"
	etcdutil "k8s.io/kubernetes/pkg/storage/etcd/util"
	"k8s.io/kubernetes/pkg/util/validation"
	"k8s.io/kubernetes/pkg/util/wait"
)

// START REDSPREAD CHANGES
func NewKube2Sky(domain, etcdServerURL, kubecfgFile, kubeMasterURL string, etcdMutationTimeout time.Duration, healthzPortIn int) func() error {
	argDomain = &domain
	argEtcdServer = &etcdServerURL
	argKubecfgFile = &kubecfgFile
	argKubeMasterURL = &kubeMasterURL
	argEtcdMutationTimeout = &etcdMutationTimeout
	healthzPort = &healthzPortIn
	return func() error {
		return main()
	}
}

// END REDSPREAD CHANGES

// The name of the "master" Kubernetes Service.
const kubernetesSvcName = "kubernetes"

var (
	// KUBERNETES CHANGE: COMMENT OUT FLAG REGISTRATION
	dummy_argDomain              = "cluster.local"
	dummy_argEtcdMutationTimeout = 10 * time.Second
	dummy_argEtcdServer          = "http://127.0.0.1:4001"
	dummy_argKubecfgFile         = ""
	dummy_argKubeMasterURL       = ""
	dummy_healthzPort            = 8081

	argDomain              = &dummy_argDomain              //flag.String("domain", "cluster.local", "domain under which to create names")
	argEtcdMutationTimeout = &dummy_argEtcdMutationTimeout //flag.Duration("etcd-mutation-timeout", 10*time.Second, "crash after retrying etcd mutation for a specified duration")
	argEtcdServer          = &dummy_argEtcdServer          //flag.String("etcd-server", "http://127.0.0.1:4001", "URL to etcd server")
	argKubecfgFile         = &dummy_argKubecfgFile         //flag.String("kubecfg-file", "", "Location of kubecfg file for access to kubernetes master service; --kube-master-url overrides the URL part of this; if neither this nor --kube-master-url are provided, defaults to service account tokens")
	argKubeMasterURL       = &dummy_argKubeMasterURL       //flag.String("kube-master-url", "", "URL to reach kubernetes master. Env variables in this flag will be expanded.")
	healthzPort            = &dummy_healthzPort            //flag.Int("healthz-port", 8081, "port on which to serve a kube2sky HTTP readiness probe.")
)

const (
	// Maximum number of attempts to connect to etcd server.
	maxConnectAttempts = 12
	// Resync period for the kube controller loop.
	resyncPeriod = 30 * time.Minute
	// A subdomain added to the user specified domain for all services.
	serviceSubdomain = "svc"
	// A subdomain added to the user specified dmoain for all pods.
	podSubdomain = "pod"
)

type etcdClient interface {
	Set(path, value string, ttl uint64) (*etcd.Response, error)
	RawGet(key string, sort, recursive bool) (*etcd.RawResponse, error)
	Delete(path string, recursive bool) (*etcd.Response, error)
}

type nameNamespace struct {
	name      string
	namespace string
}

type kube2sky struct {
	// Etcd client.
	etcdClient etcdClient
	// DNS domain name.
	domain string
	// Etcd mutation timeout.
	etcdMutationTimeout time.Duration
	// A cache that contains all the endpoints in the system.
	endpointsStore kcache.Store
	// A cache that contains all the services in the system.
	servicesStore kcache.Store
	// A cache that contains all the pods in the system.
	podsStore kcache.Store
	// Lock for controlling access to headless services.
	mlock sync.Mutex
}

// Removes 'subdomain' from etcd.
func (ks *kube2sky) removeDNS(subdomain string) error {
	glog.V(2).Infof("Removing %s from DNS", subdomain)
	resp, err := ks.etcdClient.RawGet(skymsg.Path(subdomain), false, true)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		glog.V(2).Infof("Subdomain %q does not exist in etcd", subdomain)
		return nil
	}
	_, err = ks.etcdClient.Delete(skymsg.Path(subdomain), true)
	return err
}

func (ks *kube2sky) writeSkyRecord(subdomain string, data string) error {
	// Set with no TTL, and hope that kubernetes events are accurate.
	_, err := ks.etcdClient.Set(skymsg.Path(subdomain), data, uint64(0))
	return err
}

// Generates skydns records for a headless service.
func (ks *kube2sky) newHeadlessService(subdomain string, service *kapi.Service) error {
	// Create an A record for every pod in the service.
	// This record must be periodically updated.
	// Format is as follows:
	// For a service x, with pods a and b create DNS records,
	// a.x.ns.domain. and, b.x.ns.domain.
	ks.mlock.Lock()
	defer ks.mlock.Unlock()
	key, err := kcache.MetaNamespaceKeyFunc(service)
	if err != nil {
		return err
	}
	e, exists, err := ks.endpointsStore.GetByKey(key)
	if err != nil {
		return fmt.Errorf("failed to get endpoints object from endpoints store - %v", err)
	}
	if !exists {
		glog.V(1).Infof("Could not find endpoints for service %q in namespace %q. DNS records will be created once endpoints show up.", service.Name, service.Namespace)
		return nil
	}
	if e, ok := e.(*kapi.Endpoints); ok {
		return ks.generateRecordsForHeadlessService(subdomain, e, service)
	}
	return nil
}

func getSkyMsg(ip string, port int) *skymsg.Service {
	return &skymsg.Service{
		Host:     ip,
		Port:     port,
		Priority: 10,
		Weight:   10,
		Ttl:      30,
	}
}

func (ks *kube2sky) generateRecordsForHeadlessService(subdomain string, e *kapi.Endpoints, svc *kapi.Service) error {
	glog.V(4).Infof("Endpoints Annotations: %v", e.Annotations)
	for idx := range e.Subsets {
		for subIdx := range e.Subsets[idx].Addresses {
			endpointIP := e.Subsets[idx].Addresses[subIdx].IP
			b, err := json.Marshal(getSkyMsg(endpointIP, 0))
			if err != nil {
				return err
			}
			recordValue := string(b)
			recordLabel := getHash(recordValue)
			if serializedPodHostnames := e.Annotations[endpoints.PodHostnamesAnnotation]; len(serializedPodHostnames) > 0 {
				podHostnames := map[string]endpoints.HostRecord{}
				err := json.Unmarshal([]byte(serializedPodHostnames), &podHostnames)
				if err != nil {
					return err
				}
				if hostRecord, exists := podHostnames[string(endpointIP)]; exists {
					if len(validation.IsDNS1123Label(hostRecord.HostName)) == 0 {
						recordLabel = hostRecord.HostName
					}
				}
			}
			recordKey := buildDNSNameString(subdomain, recordLabel)

			glog.V(2).Infof("Setting DNS record: %v -> %q\n", recordKey, recordValue)
			if err := ks.writeSkyRecord(recordKey, recordValue); err != nil {
				return err
			}
			for portIdx := range e.Subsets[idx].Ports {
				endpointPort := &e.Subsets[idx].Ports[portIdx]
				portSegment := buildPortSegmentString(endpointPort.Name, endpointPort.Protocol)
				if portSegment != "" {
					err := ks.generateSRVRecord(subdomain, portSegment, recordLabel, recordKey, int(endpointPort.Port))
					if err != nil {
						return err
					}
				}
			}
		}
	}

	return nil
}

func (ks *kube2sky) getServiceFromEndpoints(e *kapi.Endpoints) (*kapi.Service, error) {
	key, err := kcache.MetaNamespaceKeyFunc(e)
	if err != nil {
		return nil, err
	}
	obj, exists, err := ks.servicesStore.GetByKey(key)
	if err != nil {
		return nil, fmt.Errorf("failed to get service object from services store - %v", err)
	}
	if !exists {
		glog.V(1).Infof("could not find service for endpoint %q in namespace %q", e.Name, e.Namespace)
		return nil, nil
	}
	if svc, ok := obj.(*kapi.Service); ok {
		return svc, nil
	}
	return nil, fmt.Errorf("got a non service object in services store %v", obj)
}

func (ks *kube2sky) addDNSUsingEndpoints(subdomain string, e *kapi.Endpoints) error {
	ks.mlock.Lock()
	defer ks.mlock.Unlock()
	svc, err := ks.getServiceFromEndpoints(e)
	if err != nil {
		return err
	}
	if svc == nil || kapi.IsServiceIPSet(svc) {
		// No headless service found corresponding to endpoints object.
		return nil
	}
	// Remove existing DNS entry.
	if err := ks.removeDNS(subdomain); err != nil {
		return err
	}
	return ks.generateRecordsForHeadlessService(subdomain, e, svc)
}

func (ks *kube2sky) handleEndpointAdd(obj interface{}) {
	if e, ok := obj.(*kapi.Endpoints); ok {
		name := buildDNSNameString(ks.domain, serviceSubdomain, e.Namespace, e.Name)
		ks.mutateEtcdOrDie(func() error { return ks.addDNSUsingEndpoints(name, e) })
	}
}

func (ks *kube2sky) handlePodCreate(obj interface{}) {
	if e, ok := obj.(*kapi.Pod); ok {
		// If the pod ip is not yet available, do not attempt to create.
		if e.Status.PodIP != "" {
			name := buildDNSNameString(ks.domain, podSubdomain, e.Namespace, santizeIP(e.Status.PodIP))
			ks.mutateEtcdOrDie(func() error { return ks.generateRecordsForPod(name, e) })
		}
	}
}

func (ks *kube2sky) handlePodUpdate(old interface{}, new interface{}) {
	oldPod, okOld := old.(*kapi.Pod)
	newPod, okNew := new.(*kapi.Pod)

	// Validate that the objects are good
	if okOld && okNew {
		if oldPod.Status.PodIP != newPod.Status.PodIP {
			ks.handlePodDelete(oldPod)
			ks.handlePodCreate(newPod)
		}
	} else if okNew {
		ks.handlePodCreate(newPod)
	} else if okOld {
		ks.handlePodDelete(oldPod)
	}
}

func (ks *kube2sky) handlePodDelete(obj interface{}) {
	if e, ok := obj.(*kapi.Pod); ok {
		if e.Status.PodIP != "" {
			name := buildDNSNameString(ks.domain, podSubdomain, e.Namespace, santizeIP(e.Status.PodIP))
			ks.mutateEtcdOrDie(func() error { return ks.removeDNS(name) })
		}
	}
}

func (ks *kube2sky) generateRecordsForPod(subdomain string, service *kapi.Pod) error {
	b, err := json.Marshal(getSkyMsg(service.Status.PodIP, 0))
	if err != nil {
		return err
	}
	recordValue := string(b)
	recordLabel := getHash(recordValue)
	recordKey := buildDNSNameString(subdomain, recordLabel)

	glog.V(2).Infof("Setting DNS record: %v -> %q, with recordKey: %v\n", subdomain, recordValue, recordKey)
	if err := ks.writeSkyRecord(recordKey, recordValue); err != nil {
		return err
	}

	return nil
}

func (ks *kube2sky) generateRecordsForPortalService(subdomain string, service *kapi.Service) error {
	b, err := json.Marshal(getSkyMsg(service.Spec.ClusterIP, 0))
	if err != nil {
		return err
	}
	recordValue := string(b)
	recordLabel := getHash(recordValue)
	recordKey := buildDNSNameString(subdomain, recordLabel)

	glog.V(2).Infof("Setting DNS record: %v -> %q, with recordKey: %v\n", subdomain, recordValue, recordKey)
	if err := ks.writeSkyRecord(recordKey, recordValue); err != nil {
		return err
	}
	// Generate SRV Records
	for i := range service.Spec.Ports {
		port := &service.Spec.Ports[i]
		portSegment := buildPortSegmentString(port.Name, port.Protocol)
		if portSegment != "" {
			err = ks.generateSRVRecord(subdomain, portSegment, recordLabel, subdomain, int(port.Port))
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func santizeIP(ip string) string {
	return strings.Replace(ip, ".", "-", -1)
}

func buildPortSegmentString(portName string, portProtocol kapi.Protocol) string {
	if portName == "" {
		// we don't create a random name
		return ""
	}

	if portProtocol == "" {
		glog.Errorf("Port Protocol not set. port segment string cannot be created.")
		return ""
	}

	return fmt.Sprintf("_%s._%s", portName, strings.ToLower(string(portProtocol)))
}

func (ks *kube2sky) generateSRVRecord(subdomain, portSegment, recordName, cName string, portNumber int) error {
	recordKey := buildDNSNameString(subdomain, portSegment, recordName)
	srv_rec, err := json.Marshal(getSkyMsg(cName, portNumber))
	if err != nil {
		return err
	}
	if err := ks.writeSkyRecord(recordKey, string(srv_rec)); err != nil {
		return err
	}
	return nil
}

func (ks *kube2sky) addDNS(subdomain string, service *kapi.Service) error {
	// if ClusterIP is not set, a DNS entry should not be created
	if !kapi.IsServiceIPSet(service) {
		return ks.newHeadlessService(subdomain, service)
	}
	if len(service.Spec.Ports) == 0 {
		glog.Info("Unexpected service with no ports, this should not have happend: %v", service)
	}
	return ks.generateRecordsForPortalService(subdomain, service)
}

// Implements retry logic for arbitrary mutator. Crashes after retrying for
// etcd-mutation-timeout.
func (ks *kube2sky) mutateEtcdOrDie(mutator func() error) {
	timeout := time.After(ks.etcdMutationTimeout)
	for {
		select {
		case <-timeout:
			glog.Fatalf("Failed to mutate etcd for %v using mutator: %v", ks.etcdMutationTimeout, mutator)
		default:
			if err := mutator(); err != nil {
				delay := 50 * time.Millisecond
				glog.V(1).Infof("Failed to mutate etcd using mutator: %v due to: %v. Will retry in: %v", mutator, err, delay)
				time.Sleep(delay)
			} else {
				return
			}
		}
	}
}

func buildDNSNameString(labels ...string) string {
	var res string
	for _, label := range labels {
		if res == "" {
			res = label
		} else {
			res = fmt.Sprintf("%s.%s", label, res)
		}
	}
	return res
}

// Returns a cache.ListWatch that gets all changes to services.
func createServiceLW(kubeClient *kclient.Client) *kcache.ListWatch {
	return kcache.NewListWatchFromClient(kubeClient, "services", kapi.NamespaceAll, kselector.Everything())
}

// Returns a cache.ListWatch that gets all changes to endpoints.
func createEndpointsLW(kubeClient *kclient.Client) *kcache.ListWatch {
	return kcache.NewListWatchFromClient(kubeClient, "endpoints", kapi.NamespaceAll, kselector.Everything())
}

// Returns a cache.ListWatch that gets all changes to pods.
func createEndpointsPodLW(kubeClient *kclient.Client) *kcache.ListWatch {
	return kcache.NewListWatchFromClient(kubeClient, "pods", kapi.NamespaceAll, kselector.Everything())
}

func (ks *kube2sky) newService(obj interface{}) {
	if s, ok := obj.(*kapi.Service); ok {
		name := buildDNSNameString(ks.domain, serviceSubdomain, s.Namespace, s.Name)
		ks.mutateEtcdOrDie(func() error { return ks.addDNS(name, s) })
	}
}

func (ks *kube2sky) removeService(obj interface{}) {
	if s, ok := obj.(*kapi.Service); ok {
		name := buildDNSNameString(ks.domain, serviceSubdomain, s.Namespace, s.Name)
		ks.mutateEtcdOrDie(func() error { return ks.removeDNS(name) })
	}
}

func (ks *kube2sky) updateService(oldObj, newObj interface{}) {
	// TODO: We shouldn't leave etcd in a state where it doesn't have a
	// record for a Service. This removal is needed to completely clean
	// the directory of a Service, which has SRV records and A records
	// that are hashed according to oldObj. Unfortunately, this is the
	// easiest way to purge the directory.
	ks.removeService(oldObj)
	ks.newService(newObj)
}

func newEtcdClient(etcdServer string) (*etcd.Client, error) {
	var (
		client *etcd.Client
		err    error
	)
	for attempt := 1; attempt <= maxConnectAttempts; attempt++ {
		if _, err = etcdutil.GetEtcdVersion(etcdServer); err == nil {
			break
		}
		if attempt == maxConnectAttempts {
			break
		}
		glog.Infof("[Attempt: %d] Attempting access to etcd after 5 second sleep", attempt)
		time.Sleep(5 * time.Second)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to connect to etcd server: %v, error: %v", etcdServer, err)
	}
	glog.Infof("Etcd server found: %v", etcdServer)

	// loop until we have > 0 machines && machines[0] != ""
	poll, timeout := 1*time.Second, 10*time.Second
	if err := wait.Poll(poll, timeout, func() (bool, error) {
		if client = etcd.NewClient([]string{etcdServer}); client == nil {
			return false, fmt.Errorf("etcd.NewClient returned nil")
		}
		client.SyncCluster()
		machines := client.GetCluster()
		if len(machines) == 0 || len(machines[0]) == 0 {
			return false, nil
		}
		return true, nil
	}); err != nil {
		return nil, fmt.Errorf("Timed out after %s waiting for at least 1 synchronized etcd server in the cluster. Error: %v", timeout, err)
	}
	return client, nil
}

func expandKubeMasterURL() (string, error) {
	parsedURL, err := url.Parse(os.ExpandEnv(*argKubeMasterURL))
	if err != nil {
		return "", fmt.Errorf("failed to parse --kube-master-url %s - %v", *argKubeMasterURL, err)
	}
	if parsedURL.Scheme == "" || parsedURL.Host == "" || parsedURL.Host == ":" {
		return "", fmt.Errorf("invalid --kube-master-url specified %s", *argKubeMasterURL)
	}
	return parsedURL.String(), nil
}

// TODO: evaluate using pkg/client/clientcmd
func newKubeClient() (*kclient.Client, error) {
	var (
		config    *restclient.Config
		err       error
		masterURL string
	)
	// If the user specified --kube-master-url, expand env vars and verify it.
	if *argKubeMasterURL != "" {
		masterURL, err = expandKubeMasterURL()
		if err != nil {
			return nil, err
		}
	}

	if masterURL != "" && *argKubecfgFile == "" {
		// Only --kube-master-url was provided.
		config = &restclient.Config{
			Host:          masterURL,
			ContentConfig: restclient.ContentConfig{GroupVersion: &unversioned.GroupVersion{Version: "v1"}},
		}
	} else {
		// We either have:
		//  1) --kube-master-url and --kubecfg-file
		//  2) just --kubecfg-file
		//  3) neither flag
		// In any case, the logic is the same.  If (3), this will automatically
		// fall back on the service account token.
		overrides := &kclientcmd.ConfigOverrides{}
		overrides.ClusterInfo.Server = masterURL                                     // might be "", but that is OK
		rules := &kclientcmd.ClientConfigLoadingRules{ExplicitPath: *argKubecfgFile} // might be "", but that is OK
		if config, err = kclientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig(); err != nil {
			return nil, err
		}
	}

	glog.Infof("Using %s for kubernetes master", config.Host)
	glog.Infof("Using kubernetes API %v", config.GroupVersion)
	return kclient.New(config)
}

func watchForServices(kubeClient *kclient.Client, ks *kube2sky) kcache.Store {
	serviceStore, serviceController := kframework.NewInformer(
		createServiceLW(kubeClient),
		&kapi.Service{},
		resyncPeriod,
		kframework.ResourceEventHandlerFuncs{
			AddFunc:    ks.newService,
			DeleteFunc: ks.removeService,
			UpdateFunc: ks.updateService,
		},
	)
	go serviceController.Run(wait.NeverStop)
	return serviceStore
}

func watchEndpoints(kubeClient *kclient.Client, ks *kube2sky) kcache.Store {
	eStore, eController := kframework.NewInformer(
		createEndpointsLW(kubeClient),
		&kapi.Endpoints{},
		resyncPeriod,
		kframework.ResourceEventHandlerFuncs{
			AddFunc: ks.handleEndpointAdd,
			UpdateFunc: func(oldObj, newObj interface{}) {
				// TODO: Avoid unwanted updates.
				ks.handleEndpointAdd(newObj)
			},
		},
	)

	go eController.Run(wait.NeverStop)
	return eStore
}

func watchPods(kubeClient *kclient.Client, ks *kube2sky) kcache.Store {
	eStore, eController := kframework.NewInformer(
		createEndpointsPodLW(kubeClient),
		&kapi.Pod{},
		resyncPeriod,
		kframework.ResourceEventHandlerFuncs{
			AddFunc: ks.handlePodCreate,
			UpdateFunc: func(oldObj, newObj interface{}) {
				ks.handlePodUpdate(oldObj, newObj)
			},
			DeleteFunc: ks.handlePodDelete,
		},
	)

	go eController.Run(wait.NeverStop)
	return eStore
}

func getHash(text string) string {
	h := fnv.New32a()
	h.Write([]byte(text))
	return fmt.Sprintf("%x", h.Sum32())
}

// waitForKubernetesService waits for the "Kuberntes" master service.
// Since the health probe on the kube2sky container is essentially an nslookup
// of this service, we cannot serve any DNS records if it doesn't show up.
// Once the Service is found, we start replying on this containers readiness
// probe endpoint.
func waitForKubernetesService(client *kclient.Client) (svc *kapi.Service) {
	name := fmt.Sprintf("%v/%v", kapi.NamespaceDefault, kubernetesSvcName)
	glog.Infof("Waiting for service: %v", name)
	var err error
	servicePollInterval := 1 * time.Second
	for {
		svc, err = client.Services(kapi.NamespaceDefault).Get(kubernetesSvcName)
		if err != nil || svc == nil {
			glog.Infof("Ignoring error while waiting for service %v: %v. Sleeping %v before retrying.", name, err, servicePollInterval)
			time.Sleep(servicePollInterval)
			continue
		}
		break
	}
	return
}

// setupHealthzHandlers sets up a readiness and liveness endpoint for kube2sky.
func setupHealthzHandlers(ks *kube2sky) {
	http.HandleFunc("/readiness", func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprintf(w, "ok\n")
	})
}

// REDSPREAD CHANGE: main returning error
func main() error {
	// REDSPREAD CHANGE: Removed flag initialization
	var err error
	// TODO: Validate input flags.
	domain := *argDomain
	if !strings.HasSuffix(domain, ".") {
		domain = fmt.Sprintf("%s.", domain)
	}
	ks := kube2sky{
		domain:              domain,
		etcdMutationTimeout: *argEtcdMutationTimeout,
	}
	if ks.etcdClient, err = newEtcdClient(*argEtcdServer); err != nil {
		glog.Fatalf("Failed to create etcd client - %v", err)
	}

	kubeClient, err := newKubeClient()
	if err != nil {
		glog.Fatalf("Failed to create a kubernetes client: %v", err)
	}
	// Wait synchronously for the Kubernetes service and add a DNS record for it.
	ks.newService(waitForKubernetesService(kubeClient))
	glog.Infof("Successfully added DNS record for Kubernetes service.")

	ks.endpointsStore = watchEndpoints(kubeClient, &ks)
	ks.servicesStore = watchForServices(kubeClient, &ks)
	ks.podsStore = watchPods(kubeClient, &ks)

	// We declare kube2sky ready when:
	// 1. It has retrieved the Kubernetes master service from the apiserver. If this
	//    doesn't happen skydns will fail its liveness probe assuming that it can't
	//    perform any cluster local DNS lookups.
	// 2. It has setup the 3 watches above.
	// Once ready this container never flips to not-ready.
	setupHealthzHandlers(&ks)
	return http.ListenAndServe(fmt.Sprintf(":%d", *healthzPort), nil)
}
