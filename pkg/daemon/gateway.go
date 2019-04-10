package daemon

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/projectcalico/felix/ipsets"
	"github.com/thoas/go-funk"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog"
)

const (
	SubnetSet   = "subnets"
	LocalPodSet = "local-pod-ip"
	IPSetPrefix = "ovn"
	NATRule     = "-m set --match-set ovn40local-pod-ip src -m set ! --match-set ovn40subnets dst -j MASQUERADE"
)

func (c *Controller) runGateway(stopCh <-chan struct{}) error {
	klog.Info("start gateway")
	subnets, err := c.getSubnets()
	if err != nil {
		klog.Errorf("get subnets failed, %+v", err)
		return err
	}
	localPodIPs, err := c.getLocalPodIPs()
	if err != nil {
		klog.Errorf("get local pod ips failed, %+v", err)
		return err
	}
	c.ipSetsMgr.AddOrReplaceIPSet(ipsets.IPSetMetadata{
		MaxSize: 1048576,
		SetID:   SubnetSet,
		Type:    ipsets.IPSetTypeHashNet,
	}, subnets)
	c.ipSetsMgr.AddOrReplaceIPSet(ipsets.IPSetMetadata{
		MaxSize: 1048576,
		SetID:   LocalPodSet,
		Type:    ipsets.IPSetTypeHashIP,
	}, localPodIPs)
	c.ipSetsMgr.ApplyUpdates()

	exist, err := c.iptablesMgr.Exists("nat", "POSTROUTING", strings.Split(NATRule, " ")...)
	if err != nil {
		klog.Errorf("check iptable rule failed, %+v", err)
		return err
	}
	if !exist {
		err = c.iptablesMgr.AppendUnique("nat", "POSTROUTING", strings.Split(NATRule, " ")...)
		if err != nil {
			klog.Errorf("append iptable rule failed, %+v", err)
			return err
		}
	}

	ticker := time.NewTicker(3 * time.Second)
LOOP:
	for {
		select {
		case <-stopCh:
			klog.Info("exit gateway")
			break LOOP
		case <-ticker.C:
			klog.V(5).Info("tick")
		}
		subnets, err := c.getSubnets()
		if err != nil {
			klog.Errorf("get subnets failed, %+v", err)
			continue
		}
		localPodIPs, err := c.getLocalPodIPs()
		if err != nil {
			klog.Errorf("get local pod ips failed, %+v", err)
			continue
		}

		c.ipSetsMgr.AddOrReplaceIPSet(ipsets.IPSetMetadata{
			MaxSize: 1048576,
			SetID:   SubnetSet,
			Type:    ipsets.IPSetTypeHashNet,
		}, subnets)
		c.ipSetsMgr.AddOrReplaceIPSet(ipsets.IPSetMetadata{
			MaxSize: 1048576,
			SetID:   LocalPodSet,
			Type:    ipsets.IPSetTypeHashIP,
		}, localPodIPs)
		c.ipSetsMgr.ApplyUpdates()
	}
	return nil
}

func (c *Controller) getLocalPodIPs() ([]string, error) {
	var localPodIPs []string
	hostname, _ := os.Hostname()
	allPods, err := c.podsLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("list pods failed, %+v", err)
		return nil, err
	}
	for _, pod := range allPods {
		if pod.Spec.NodeName == hostname && pod.Spec.HostNetwork != true && pod.Status.PodIP != "" {
			localPodIPs = append(localPodIPs, pod.Status.PodIP)
		}
	}
	klog.V(5).Infof("local pod ips %v", localPodIPs)
	return localPodIPs, nil
}

func (c *Controller) getSubnets() ([]string, error) {
	var subnets []string
	output, err := c.ovnClient.ListLogicalRouterPort()
	if err != nil {
		klog.Errorf("list logical router port failed, %+v", err)
		return nil, err
	}
	outputs := funk.FilterString(strings.Split(output, "\n"), func(s string) bool {
		return s != ""
	})
	/*
		ovn-cluster-join
		100.64.0.1/16

		ovn-cluster-ovn-default
		10.16.0.1/16
	*/
	chucks := funk.Chunk(outputs, 2).([][]string)
	for _, chuck := range chucks {
		name := chuck[0]
		network := chuck[1]
		if name != fmt.Sprintf("%s-%s", c.config.ClusterRouter, c.config.NodeSwitch) {
			subnets = append(subnets, network)
		}
	}
	klog.V(5).Infof("subnets %v", subnets)
	return subnets, nil
}