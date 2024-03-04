package device_plugin

import (
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	klog "k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const (
	xdxctVendorId   = "1eed"
	xdxctPGPUDriver = "vfio-pci"
)

type XdxctGpuDevice struct {
	addr string
}

// iommuMap key: iommu_group value: pcie-addr
var iommuMap map[string][]XdxctGpuDevice

// deviceMap key: deviceID value: iommu_group
var deviceMap map[string][]string

// key: vGpu type value: the list of vgpu uuid
var vGpuMap map[string][]XdxctGpuDevice

// key: xdxct Gpu id value: the list of vgpu uuid
var gpuVgpuMap map[string][]string

var basePciPath = "/sys/bus/pci/devices"

var vGpuBasePath = "/sys/bus/mdev/devices"

var readLink = readLinkFunc
var stop = make(chan struct{})

func InitiateDevicePlugin() {
	createIommuDeviceMap()
	createVgpuMap()
	createDevicePlugins()
}

func createDevicePlugins() {
	var devicePlugins []*GenericDevicePlugin
	var vgpuDevicePlugins []*GenericVgpuDevicePlugin
	var devs []*pluginapi.Device
	var deviceName string
	log.Printf("Device Map %s", deviceMap)
	for k, v := range deviceMap {
		devs = nil
		for _, dev := range v {
			devs = append(devs, &pluginapi.Device{
				ID:     dev,
				Health: pluginapi.Healthy,
			})
		}
		deviceName = k
		log.Printf("Device Name: %s", deviceName)
		dp := NewGenericaDevicePlugin(deviceName, "/sys/kernel/iommu_groups/", devs)
		err := startDevicePlugin(dp)
		if err != nil {
			log.Printf("Error starting %s device plugin: %v", dp.deviceName, err)
		} else {
			devicePlugins = append(devicePlugins, dp)
		}
	}

	for k, v := range vGpuMap {
		devs = nil
		for _, dev := range v {
			devs = append(devs, &pluginapi.Device{
				ID:     dev.addr,
				Health: pluginapi.Healthy,
			})
		}
		deviceName = k
		log.Printf("vGPU Device name: %s", deviceName)
		dp := NewGenericaVgpuDevicePlugin(deviceName, vGpuBasePath, devs)
		err := startVgpuDevicePlugin(dp)
		if err != nil {
			log.Printf("Error starting %s device plugin: %v", dp.deviceName, err)
		} else {
			vgpuDevicePlugins = append(vgpuDevicePlugins, dp)
		}
	}

	<-stop
	log.Println("Shutting down device plugin controller")
	for _, v := range devicePlugins {
		v.Stop()
	}

	for _, v := range vgpuDevicePlugins {
		v.Stop()
	}
}

// Discovers all xdxct gpus which are loaded with VFIO-PCI driver and create corresponding maps
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

// Discovers all xdxct vgpus and create corresponding maps
func createVgpuMap() {
	vGpuMap = make(map[string][]XdxctGpuDevice)
	gpuVgpuMap = make(map[string][]string)

	filepath.Walk(vGpuBasePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("Error accessing file path %q:%v", path, err)
			return err
		}
		if info.IsDir() {
			log.Printf("Not a device, continuing")
			return nil
		}
		vGpuId, err := readVgpuIDFromFile(vGpuBasePath, info.Name(), "mdev_type/name")
		if err != nil {
			log.Println("Could not get vgpu type identifier for device", info.Name())
			return nil
		}
		gpuId, err := readGpuIDFromVgpu(vGpuBasePath, info.Name())
		if err != nil {
			log.Println("Could not get device id", info.Name())
		}
		gpuVgpuMap[gpuId] = append(gpuVgpuMap[gpuId], info.Name())
		vGpuMap[vGpuId] = append(vGpuMap[vGpuId], XdxctGpuDevice{info.Name()})
		log.Printf("GPU MAP is %v", gpuVgpuMap)
		log.Printf("VGPU MAP is %s", vGpuMap)
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

func readVgpuIDFromFile(basePath string, deviceAddress string, property string) (string, error) {
	reg := regexp.MustCompile(`Type Name: (\w+)`)
	data, err := os.ReadFile(filepath.Join(basePath, deviceAddress, property))
	if err != nil {
		klog.Errorf("filed to get %s of device %s: %v", property, deviceAddress, err)
		return "", err
	}
	str := strings.Trim(string(data[:]), "\n")
	matches := reg.FindStringSubmatch(str)
	if len(matches) > 1 {
		extracted := matches[1]
		return extracted, nil
	} else {
		klog.Errorf("No match found: %v", err)
		return "", nil
	}
}

func readGpuIDFromVgpu(basePath string, deviceAddress string) (string, error) {
	path, err := os.Readlink(filepath.Join(basePath, deviceAddress))
	if err != nil {
		klog.Errorf("filed to read link for device %s: %v", deviceAddress, err)
		return "", err
	}

	strArray := strings.Split(path, "/")
	length := len(strArray)
	str := strings.Trim(strArray[length-2], "\n")
	return str, nil
}
func getIommuMap() map[string][]XdxctGpuDevice {
	return iommuMap
}

func startDevicePlugin(dp *GenericDevicePlugin) error {
	return dp.Start(stop)
}

func startVgpuDevicePlugin(dp *GenericVgpuDevicePlugin) error {
	return dp.Start(stop)
}
