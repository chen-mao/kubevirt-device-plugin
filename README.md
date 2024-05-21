# XDXCT Kubevirt-device-plugin to assign GPUs and vGPUs to kubevirt VMs

## About
This is a kubevirt device plugin that can discover and expose GPUs and vGPUs on a kubernetes node.Its specifically developed to serve kubevirt workloads in kubernetes cluster.

## Features
- Discovers XDXCT GPUs which  are bound to VFIO-PCI driver and exposes them as devices available to be attached to VM in pass through mode.
- Discovers XDXCT vGPUs configured on a kubernetes node and exposes them to be attached to Kubevirt VMs

## Docs
### Deployment
The daemonset creation yaml can be used to deploy the device plugin.
```shell
kubectl apply -f xdxct-kubevirt-device-plugin.yaml
```
Examples yamls for creating VMs with GPU/vGPU are in the examples folder.
### Build
Build executable binary using make
```shell
make build
```
Build docker image
```shell
make build-image DOCKER_REPO=<docker-repo-url>  DOCKER_IMAGE_TAG=<image-tag>
```
Push docker-image to xdxct harbor
```shell
make push-image DOCKER_REPO=<docker-repo-url>  DOCKER_IMAGE_TAG=<image-tag>
```
