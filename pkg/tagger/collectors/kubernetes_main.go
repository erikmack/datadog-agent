// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2018 Datadog, Inc.

// +build kubeapiserver,kubelet

package collectors

import (
	"fmt"
	"time"

	log "github.com/cihub/seelog"

	"github.com/DataDog/datadog-agent/pkg/config"
	"github.com/DataDog/datadog-agent/pkg/errors"
	"github.com/DataDog/datadog-agent/pkg/util/clusteragent"
	"github.com/DataDog/datadog-agent/pkg/util/kubernetes/apiserver"
	"github.com/DataDog/datadog-agent/pkg/util/kubernetes/kubelet"
)

const (
	kubeServiceCollectorName = "kube-service-collector"
)

type KubeServiceCollector struct {
	kubeUtil  *kubelet.KubeUtil
	apiClient *apiserver.APIClient
	infoOut   chan<- []*TagInfo
	dcaClient *clusteragent.DCAClient
	// used to set a custom delay
	lastUpdate time.Time
	updateFreq time.Duration
}

// Detect tries to connect to the kubelet and the API Server if the DCA is not used or the DCA.
func (c *KubeServiceCollector) Detect(out chan<- []*TagInfo) (CollectionMode, error) {
	if config.Datadog.GetBool("kubernetes_collect_service_tags") == false {
		return NoCollection, fmt.Errorf("collection disabled by the configuration")
	}

	var err, errDCA error
	c.kubeUtil, err = kubelet.GetKubeUtil()
	if err != nil {
		return NoCollection, err
	}
	// if no DCA or can't communicate with the DCA run the local service mapper.
	if config.Datadog.GetBool("cluster_agent") {
		c.dcaClient, errDCA = clusteragent.GetClusterAgentClient()
		if errDCA != nil {
			log.Errorf("Could not initialise the communication with the DCA, falling back to local service mapping: %s", errDCA.Error())
		}
	}
	if !config.Datadog.GetBool("cluster_agent") || errDCA != nil {
		c.apiClient, err = apiserver.GetAPIClient()
		if err != nil {
			return NoCollection, err
		}
		c.apiClient.StartServiceMapping()
	}

	c.infoOut = out
	c.updateFreq = time.Duration(config.Datadog.GetInt("kubernetes_service_tag_update_freq")) * time.Second
	return PullCollection, nil
}

// Pull implements an additional time constraints to avoid exhausting the kube-apiserver
func (c *KubeServiceCollector) Pull() error {
	// Time constraints, get the delta in seconds to display it in the logs:
	timeDelta := c.lastUpdate.Add(c.updateFreq).Unix() - time.Now().Unix()
	if timeDelta > 0 {
		log.Tracef("skipping, next effective Pull will be in %s seconds", timeDelta)
		return nil
	}

	pods, err := c.kubeUtil.GetLocalPodList()
	if err != nil {
		return err
	}
	if !config.Datadog.GetBool("cluster_agent") {
		// If the DCA is not used, each agent stores a local cache of the ServiceMap.
		err = c.addToCacheServiceMapping(pods)
		if err != nil {
			log.Debugf("Cannot add the serviceMapping to cache: %s", err)
		}
	}
	c.infoOut <- c.getTagInfos(pods)
	c.lastUpdate = time.Now()
	return nil
}

// Fetch fetches tags for a given entity by iterating on the whole podlist and
// the serviceMapper
func (c *KubeServiceCollector) Fetch(entity string) ([]string, []string, error) {
	var lowCards, highCards []string

	pod, err := c.kubeUtil.GetPodForEntityID(entity)
	if err != nil {
		return lowCards, highCards, err
	}

	if kubelet.IsPodReady(pod) == false {
		return lowCards, highCards, errors.NewNotFound(entity)
	}

	pods := []*kubelet.Pod{pod}
	if !config.Datadog.GetBool("cluster_agent") {
		// If the DCA is not used, each agent stores a local cache of the ServiceMap.
		err = c.addToCacheServiceMapping(pods)
		if err != nil {
			log.Debugf("Cannot add the serviceMapping to cache: %s", err)
		}
	}

	tagInfos := c.getTagInfos(pods)
	c.infoOut <- tagInfos
	for _, info := range tagInfos {
		if info.Entity == entity {
			return info.LowCardTags, info.HighCardTags, nil
		}
	}
	return lowCards, highCards, errors.NewNotFound(entity)
}

func kubernetesFactory() Collector {
	return &KubeServiceCollector{}
}

func init() {
	registerCollector(kubeServiceCollectorName, kubernetesFactory, HighPriority)
}
