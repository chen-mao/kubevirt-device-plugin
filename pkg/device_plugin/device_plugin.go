package device_plugin

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	klog "k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const (
	xdxctVendorId   = "1eed"
	xdxctPGPUDriver = "vfio-pci"
)

type XdxctGpuDevice struct {
	addr string // device pci address
}

// iommuMap key: iommu_group value: pcie-addr
// deviceMap key: deviceID value: iommu_group
var iommuMap map[string][]XdxctGpuDevice
var deviceMap map[string][]string
var basePciPath = "/sys/bus/pci/devices"
var readLink = readLinkFunc
var stop = make(chan struct{})

func InitiateDevicePlugin() {
	createIommuDeviceMap()
	createDevicePlugins()
}

func createDevicePlugins() {
	var devicePlugins []*GenericDevicePlugin
	var devs []*pluginapi.Device
	log.Printf("Device Map %s", deviceMap)
	devs = nil
	for k, v := range deviceMap {
		for _, dev := range v {
			devs = append(devs, &pluginapi.Device{
				ID:     dev,
				Health: pluginapi.Healthy,
			})
		}
		deviceName := getDeviceName(k)
		if deviceName == "" {
			log.Printf("Error: Cloud not find device Name for device ID: %s", k)
			deviceName = k
		}
		log.Printf("Device Name: %s", deviceName)
		dp := NewGenericaDevicePlugin(deviceName, "/sys/kernel/iommu_groups/", devs)
		err := startDevicePlugin(dp)
		if err != nil {
			log.Printf("Error starting %s device plugin: %v", dp.deviceName, err)
		} else {
			devicePlugins = append(devicePlugins, dp)
		}
	}

	<-stop
	log.Println("Shutting down device plugin controller")
	for _, v := range devicePlugins {
		v.Stop()
	}
}

func createIommuDeviceMap() {
	iommuMap = make(map[string][]XdxctGpuDevice)
	deviceMap = make(map[string][]string)
	// find pci devices
	filepath.Walk(basePciPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("Failed to access file path %q: %v\n", path, err)
			return err
		}
		if info.IsDir() {
			return nil
		}

		vendorID, err := readIDFromFile(basePciPath, info.Name(), "vendor")
		if err != nil {
			log.Println("Cloud not get driver for device", info.Name())
			return nil
		}

		if vendorID == xdxctVendorId {
			log.Println("Xdxct device vendorID:", info.Name())
			driver, err := readLink(basePciPath, info.Name(), "driver")
			if err != nil {
				log.Println("Failed to get driver for device", info.Name())
				return nil
			}
			if driver == xdxctPGPUDriver {
				log.Println("Xdxct device driver vfio-pci:", info.Name())
				iommuGroup, err := readLink(basePciPath, info.Name(), "iommu_group")
				if err != nil {
					log.Println("Failed to get IOMMU Group for device", info.Name())
					return nil
				}
				log.Printf("IOMMU Group: %s", iommuGroup)

				if _, exists := iommuMap[iommuGroup]; !exists {
					deviceID, err := readIDFromFile(basePciPath, info.Name(), "device")
					if err != nil {
						log.Printf("Failed to get %s deviceID of pci devices", info.Name())
					}
					deviceMap[deviceID] = append(deviceMap[deviceID], iommuGroup)
				}
				iommuMap[iommuGroup] = append(iommuMap[iommuGroup], XdxctGpuDevice{info.Name()})
			}
		}
		return nil
	})
}

func readIDFromFile(basePciPath string, deviceAddress string, property string) (string, error) {
	context, err := os.ReadFile(filepath.Join(basePciPath, deviceAddress, property))
	if err != nil {
		klog.Errorf("filed to get %s of device %s: %v", property, deviceAddress, err)
		return "", err
	}
	id := strings.Trim(string(context[2:]), "\n")
	return id, err
}

func readLinkFunc(basePciPath string, deviceAddress string, link string) (string, error) {
	path, err := os.Readlink(filepath.Join(basePciPath, deviceAddress, link))
	if err != nil {
		klog.Errorf("failed to read link %s of deice %s: %v", link, deviceAddress, err)
		return "", err
	}
	_, file := filepath.Split(path)
	return file, err
}

func getIommuMap() map[string][]XdxctGpuDevice {
	return iommuMap
}

func getDeviceName(deviceID string) string {
	return "Pangu_A0"
}

func startDevicePlugin(dp *GenericDevicePlugin) error {
	return dp.Start(stop)
}
