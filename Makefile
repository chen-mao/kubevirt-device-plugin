DOCKER_REPO ?= "hub.xdxct.com/kubevirt/kubevirt-device-plugin"
DOCKER_IMAGE_TAG ?= devel

build:
	go build -o xdxct-kubevirt-device-plugin kubevirt-device-plugin/cmd
clean:
	rm -r xdxct-kubevirt-device-plugin
build-image:
	docker build . -t $(DOCKER_REPO):$(DOCKER_IMAGE_TAG)
push-image: build-image
	docker push $(DOCKER_REPO):$(DOCKER_IMAGE_TAG)