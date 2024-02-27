package device_plugin

import (
	"fmt"
	"log"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const (
	DeviceNamespace = "xdxct.com"
	vfioDevicePath  = "/dev/vfio"
	gpuPrefix       = "PCI_RESOURCE_XDXCT_COM"
	vgpuPrefix      = "MDEV_PCI_RESOURCE_XDXCT_COM"
	connectTimeOut  = 5 * time.Second
)

var returnIommuMap = getIommuMap

type GenericDevicePlugin struct {
	devs       []*pluginapi.Device
	server     *grpc.Server
	stop       chan struct{}
	term       chan bool // this channel detects kubelet restarts
	healthy    chan string
	unhealthy  chan string
	sockPath   string
	deviceName string
	devicePath string
}

func NewGenericaDevicePlugin(deviceName string, devicePath string, devices []*pluginapi.Device) *GenericDevicePlugin {
	serverSock := fmt.Sprintf(pluginapi.DevicePluginPath+"kubevirt-%s.sock", deviceName)

	return &GenericDevicePlugin{
		devs:       devices,
		sockPath:   serverSock,
		term:       make(chan bool, 1),
		healthy:    make(chan string),
		unhealthy:  make(chan string),
		deviceName: deviceName,
		devicePath: devicePath,
	}
}

func waitForGrpcServer(sockPath string, timeout time.Duration) error {
	conn, err := connect(sockPath, timeout)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

func connect(sockPath string, timeout time.Duration) (*grpc.ClientConn, error) {
	ctx, _ := context.WithTimeout(context.Background(), timeout)
	g, err := grpc.DialContext(ctx, sockPath,
		grpc.WithBlock(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDialer(func(addr string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("unix", addr, timeout)
		}),
	)
	if err != nil {
		return nil, err
	}
	return g, nil
}

func buildEnv(envList map[string][]string) map[string]string {
	env := map[string]string{}
	for key, pcieList := range envList {
		env[key] = strings.Join(pcieList, ",")
	}
	return env
}

func (dp *GenericDevicePlugin) Start(stop chan struct{}) error {
	if dp.server != nil {
		return fmt.Errorf("grpc server already start")
	}

	dp.stop = stop

	if err := dp.cleanup(); err != nil {
		return err
	}

	sock, err := net.Listen("unix", dp.sockPath)
	if err != nil {
		log.Printf("Errorf %s connect to GRPC socket: %v", dp.deviceName, err)
		return err
	}
	dp.server = grpc.NewServer([]grpc.ServerOption{}...)
	pluginapi.RegisterDevicePluginServer(dp.server, dp)

	go dp.server.Serve(sock)

	err = waitForGrpcServer(dp.sockPath, connectTimeOut)
	if err != nil {
		log.Printf("Errorf %s connect to GRPC server: %v", dp.deviceName, err)
		return err
	}

	err = dp.Register()
	if err != nil {
		log.Printf("Errorf %s register device plugin: %v", dp.deviceName, err)
	}

	go dp.healthyCheck()

	log.Println(dp.deviceName + "Device Plugin server ready")
	return nil
}

func (dp *GenericDevicePlugin) cleanup() error {
	if err := os.Remove(dp.sockPath); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

func (dp *GenericDevicePlugin) Stop() error {
	if dp.server == nil {
		return nil
	}

	dp.term <- true
	dp.server.Stop()
	dp.server = nil

	return dp.cleanup()
}

func (dp *GenericDevicePlugin) restart() error {
	log.Printf("Restarting %s device Plugin server", dp.deviceName)
	if dp.server == nil {
		return fmt.Errorf("grpc server instance not found for %s", dp.deviceName)
	}
	dp.Stop()
	var stop = make(chan struct{})
	return dp.Start(stop)
}

func (dp *GenericDevicePlugin) Register() error {
	conn, err := connect(pluginapi.KubeletSocket, connectTimeOut)
	if err != nil {
		return err
	}
	defer conn.Close()

	client := pluginapi.NewRegistrationClient(conn)
	req := &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     path.Base(dp.sockPath),
		ResourceName: fmt.Sprintf("%s/%s", DeviceNamespace, dp.deviceName),
	}

	_, err = client.Register(context.Background(), req)
	if err != nil {
		return err
	}
	return nil
}

func (dp *GenericDevicePlugin) GetDevicePluginOptions(ctx context.Context, e *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	options := &pluginapi.DevicePluginOptions{
		PreStartRequired: false,
	}
	return options, nil
}

func (dp *GenericDevicePlugin) ListAndWatch(e *pluginapi.Empty, s pluginapi.DevicePlugin_ListAndWatchServer) error {
	s.Send(&pluginapi.ListAndWatchResponse{
		Devices: dp.devs,
	})

	for {
		select {
		case unhealthy := <-dp.unhealthy:
			log.Printf("In watch unhealthy")
			for _, dev := range dp.devs {
				if unhealthy == dev.ID {
					dev.Health = pluginapi.Unhealthy
				}
			}
			s.Send(&pluginapi.ListAndWatchResponse{
				Devices: dp.devs,
			})
		case healthy := <-dp.healthy:
			log.Printf("In watch healthy")
			for _, dev := range dp.devs {
				if healthy == dev.ID {
					dev.Health = pluginapi.Healthy
				}
			}
			s.Send(&pluginapi.ListAndWatchResponse{
				Devices: dp.devs,
			})
		case <-dp.stop:
			return nil
		case <-dp.term:
			return nil
		}
	}
}

func (dp *GenericDevicePlugin) GetPreferredAllocation(ctx context.Context, in *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {
	return nil, nil
}

func (dp *GenericDevicePlugin) Allocate(ctx context.Context, reqs *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	log.Println("In allocate")
	responses := pluginapi.AllocateResponse{}
	envList := map[string][]string{}

	for _, req := range reqs.ContainerRequests {
		deviceSpecs := make([]*pluginapi.DeviceSpec, 0)
		for _, iommuId := range req.DevicesIDs {
			devAddrs := []string{}

			returnedMap := returnIommuMap()
			//Retrieve the devices associated with a Iommu group
			xdxDev := returnedMap[iommuId]
			for _, dev := range xdxDev {
				iommuGroup, err := readLink(basePciPath, dev.addr, "iommu_group")
				if err != nil || iommuGroup != iommuId {
					log.Println("IommuGroup has changed on the system ", dev.addr)
					return nil, fmt.Errorf("invalid allocation request: unknown device: %s", dev.addr)
				}
				vendorID, err := readIDFromFile(basePciPath, dev.addr, "vendor")
				if err != nil || vendorID != "1eed" {
					log.Println("Vendor has changed on the system ", dev.addr)
					return nil, fmt.Errorf("invalid allocation request: unknown device: %s", dev.addr)
				}

				devAddrs = append(devAddrs, dev.addr)

			}
			deviceSpecs = append(deviceSpecs, &pluginapi.DeviceSpec{
				HostPath:      filepath.Join(vfioDevicePath, "vfio"),
				ContainerPath: filepath.Join(vfioDevicePath, "vfio"),
				Permissions:   "mrw",
			})
			deviceSpecs = append(deviceSpecs, &pluginapi.DeviceSpec{
				HostPath:      filepath.Join(vfioDevicePath, iommuId),
				ContainerPath: filepath.Join(vfioDevicePath, iommuId),
				Permissions:   "mrw",
			})

			key := fmt.Sprintf("%s_%s", gpuPrefix, dp.deviceName)
			if _, exists := envList[key]; !exists {
				envList[key] = []string{}
			}
			envList[key] = append(envList[key], devAddrs...)
		}
		envs := buildEnv(envList)
		log.Printf("Allocated devices: %s", envs)
		response := pluginapi.ContainerAllocateResponse{
			Envs:    envs,
			Devices: deviceSpecs,
		}

		responses.ContainerResponses = append(responses.ContainerResponses, &response)
	}
	return &responses, nil
}

func (dp *GenericDevicePlugin) PreStartContainer(ctx context.Context, in *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	res := &pluginapi.PreStartContainerResponse{}
	return res, nil
}

func (dp *GenericDevicePlugin) healthyCheck() error {
	method := fmt.Sprintf("healthCheck(%s)", dp.deviceName)
	log.Printf("%s: invoked", method)
	var pathDeviceMap = make(map[string]string)
	var path = dp.devicePath
	var health = ""

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("%s: unable to create fsnotify watcher: %v", method, err)
		return err
	}
	defer watcher.Close()

	err = watcher.Add(filepath.Dir(dp.sockPath))
	if err != nil {
		log.Printf("%s: Unable to add device socket path to fsnotify watcher: %v", method, err)
		return err
	}

	_, err = os.Stat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("%s: Unable to stat device: %v", method, err)
			return err
		}
	}

	for _, dev := range dp.devs {
		devicePath := filepath.Join(path, dev.ID)
		err = watcher.Add(devicePath)
		pathDeviceMap[devicePath] = dev.ID
		if err != nil {
			log.Printf("%s: Unable to add device path to fsnotify watcher: %v", method, err)
			return err
		}
	}

	for {
		select {
		case <-dp.stop:
			return nil
		case event := <-watcher.Events:
			v, ok := pathDeviceMap[event.Name]
			if ok {
				if event.Op == fsnotify.Create {
					health = v
					dp.healthy <- health
				} else if (event.Op == fsnotify.Remove) || (event.Op == fsnotify.Rename) {
					log.Printf("%s: Marking device unhealthy: %s", method, event.Name)
					health = v
					dp.unhealthy <- health
				}
			} else if event.Name == dp.sockPath && event.Op == fsnotify.Remove {
				log.Printf("%s: Socket path for GPU device was removed, kubelet likely restarted", method)
				if err := dp.restart(); err != nil {
					log.Printf("%s: Unable to restart server %v", method, err)
					return err
				}
				log.Printf("%s: Successfully restarted %s device plugin server. Terminating.", method, dp.deviceName)
				return nil
			}
		}
	}
}
