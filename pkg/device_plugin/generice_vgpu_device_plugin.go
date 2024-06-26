package device_plugin

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
	"google.golang.org/grpc"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

type GenericVgpuDevicePlugin struct {
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

func NewGenericaVgpuDevicePlugin(deviceName string, devicePath string, devices []*pluginapi.Device) *GenericVgpuDevicePlugin {
	serverSock := fmt.Sprintf(pluginapi.DevicePluginPath+"kubevirt-%s.sock", deviceName)
	dpi := &GenericVgpuDevicePlugin{
		deviceName: deviceName,
		sockPath:   serverSock,
		devicePath: devicePath,
		devs:       devices,
		term:       make(chan bool, 1),
		healthy:    make(chan string),
		unhealthy:  make(chan string),
	}
	return dpi
}

func (dpi *GenericVgpuDevicePlugin) Start(stop chan struct{}) error {
	if dpi.server != nil {
		return fmt.Errorf("grpc server already start")
	}

	dpi.stop = stop

	if err := dpi.cleanup(); err != nil {
		return err
	}

	sock, err := net.Listen("unix", dpi.sockPath)
	if err != nil {
		log.Printf("Errof %s connect to GRPC socker: %v", dpi.deviceName, err)
		return err
	}

	dpi.server = grpc.NewServer([]grpc.ServerOption{}...)
	pluginapi.RegisterDevicePluginServer(dpi.server, dpi)

	go dpi.server.Serve(sock)

	err = waitForGrpcServer(dpi.sockPath, connectTimeOut)
	if err != nil {
		log.Printf("[%s] failed to connnect to GRPC server: %v", dpi.deviceName, err)
	}

	err = dpi.Register()
	if err != nil {
		log.Printf("[%s] failed to register with device plugin manager: %v", dpi.deviceName, err)
	}

	go dpi.healthCheck()

	log.Println(dpi.deviceName + ": Device plugin Server ready")
	return nil
}

func (dpi *GenericVgpuDevicePlugin) Register() error {
	conn, err := connect(pluginapi.KubeletSocket, connectTimeOut)
	if err != nil {
		return err
	}
	defer conn.Close()

	client := pluginapi.NewRegistrationClient(conn)
	req := &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     path.Base(dpi.sockPath),
		ResourceName: fmt.Sprintf("%s/%s", DeviceNamespace, dpi.deviceName),
	}

	_, err = client.Register(context.Background(), req)
	if err != nil {
		return err
	}
	return nil
}

func (dpi *GenericVgpuDevicePlugin) cleanup() error {
	pattern := filepath.Join(pluginapi.DevicePluginPath, "kubevirt-XGV_V0*.sock")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		fmt.Println("Error matching files:", err)
		return err
	}

	for _, match := range matches {
		err := os.Remove(match)
		if err != nil {
			fmt.Println("Error deleting file:", err)
		} else {
			fmt.Println("Deleted file:", match)
		}
	}
	return nil
}

func (dpi *GenericVgpuDevicePlugin) Stop() error {
	if dpi.server == nil {
		return nil
	}

	dpi.term <- true
	dpi.server.Stop()
	dpi.server = nil

	return dpi.cleanup()
}

func (dpi *GenericVgpuDevicePlugin) restart() error {
	log.Printf("Restarting %s device plugin server", dpi.deviceName)
	if dpi.server == nil {
		return fmt.Errorf("grpc server instance not found for %s", dpi.deviceName)
	}

	dpi.Stop()

	var stop = make(chan struct{})
	return dpi.Start(stop)
}

func (dpi *GenericVgpuDevicePlugin) GetDevicePluginOptions(ctx context.Context, e *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	options := &pluginapi.DevicePluginOptions{
		PreStartRequired: false,
	}
	return options, nil
}

func (dpi *GenericVgpuDevicePlugin) ListAndWatch(e *pluginapi.Empty, s pluginapi.DevicePlugin_ListAndWatchServer) error {
	s.Send(&pluginapi.ListAndWatchResponse{
		Devices: dpi.devs,
	})

	log.Printf("send device info: %v", dpi.devs)
	for {
		select {
		case unhealthy := <-dpi.unhealthy:
			log.Println("In watch unhealthy")
			for _, dev := range dpi.devs {
				if unhealthy == dev.ID {
					dev.Health = pluginapi.Unhealthy
				}
			}
			s.Send(&pluginapi.ListAndWatchResponse{
				Devices: dpi.devs,
			})
		case healthy := <-dpi.healthy:
			log.Println("In watch healthy")
			for _, dev := range dpi.devs {
				if healthy == dev.ID {
					dev.Health = pluginapi.Healthy
				}
			}
			s.Send(&pluginapi.ListAndWatchResponse{
				Devices: dpi.devs,
			})
		case <-dpi.stop:
			return nil
		case <-dpi.term:
			return nil
		}
	}
}

func (dpi *GenericVgpuDevicePlugin) Allocate(ctx context.Context, reqs *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	log.Println("In allocate")
	responses := pluginapi.AllocateResponse{}

	for _, req := range reqs.ContainerRequests {
		deviceSpecs := make([]*pluginapi.DeviceSpec, 0)
		envList := map[string][]string{}
		for _, str := range req.DevicesIDs {
			vGpuId, err := readVgpuIDFromFile(vGpuBasePath, str, "mdev_type/name")
			if err != nil || vGpuId != dpi.deviceName {
				log.Println("Could not get vGPU type", dpi.deviceName)
				log.Println("Could not get vGPU type", vGpuId)
				continue
			}

			key := fmt.Sprintf("%s_%s", vgpuPrefix, dpi.deviceName)
			if _, exists := envList[key]; !exists {
				envList[key] = []string{}
			}
			envList[key] = append(envList[key], str)
		}
		deviceSpecs = append(deviceSpecs, &pluginapi.DeviceSpec{
			HostPath:      vfioDevicePath,
			ContainerPath: vfioDevicePath,
			Permissions:   "mrw",
		})
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

func (dpi *GenericVgpuDevicePlugin) GetPreferredAllocation(ctx context.Context, in *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {
	return nil, nil
}

func (dpi *GenericVgpuDevicePlugin) PreStartContainer(ctx context.Context, in *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	res := &pluginapi.PreStartContainerResponse{}
	return res, nil
}

// 健康检查todo：
// 宿主机之外，通过echo "1" > uuid/remove 收不到删除的信号。
func (dpi *GenericVgpuDevicePlugin) healthCheck() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("unable to create fsnotify watcher: %v", err)
		return err
	}
	defer watcher.Close()

	err = watcher.Add(filepath.Dir(dpi.sockPath))
	if err != nil {
		log.Printf("unable to add device plugin socket path to fsnotify watcher: %v", err)
		return err
	}

	for {
		select {
		case <-dpi.stop:
			return nil
		case event := <-watcher.Events:
			fmt.Printf("event name: %s, op: %v\n", event.Name, event.Op)
		}
	}
}

// func (dpi *GenericVgpuDevicePlugin) healthCheck() error {
// 	var health string
// 	var path = dpi.devicePath
// 	var pathVGPUDevice = make(map[string]string)

// 	watcher, err := fsnotify.NewWatcher()
// 	if err != nil {
// 		log.Printf("unable to create fsnotify watcher: %v", err)
// 		return err
// 	}
// 	defer watcher.Close()

// 	err = watcher.Add(filepath.Dir(dpi.sockPath))
// 	if err != nil {
// 		log.Printf("unable to add device plugin socket path to fsnotify watcher: %v", err)
// 		return err
// 	}

// 	_, err = os.Stat(path)
// 	if err != nil {
// 		if os.IsNotExist(err) {
// 			log.Printf("unable to stat devie: %v", err)
// 			return err
// 		}
// 	}

// 	for _, dev := range dpi.devs {
// 		devicePath := filepath.Join(path, dev.ID)
// 		log.Printf("Adding watch for device path: %s", devicePath)
// 		err = watcher.Add(devicePath)
// 		pathVGPUDevice[devicePath] = dev.ID
// 		if err != nil {
// 			log.Printf("unable to add device path to fsnotify watcher: %v", err)
// 			return err
// 		}
// 	}

// 	for {
// 		select {
// 		case <-dpi.stop:
// 			return nil
// 		case event := <-watcher.Events:
// 			v, ok := pathVGPUDevice[event.Name]
// 			fmt.Printf("event name: %s, op: %v\n", event.Name, event.Op)
// 			if ok {
// 				if event.Op == fsnotify.Write {
// 					log.Printf("Marking device unhealthy: %s", event.Name)
// 					health = v
// 					dpi.unhealthy <- health
// 				}
// 			}
// 		}
// 	}
// }
