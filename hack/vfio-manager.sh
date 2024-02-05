#!/bin/bash

set -eu

usage()
{
    cat >&2<<EOF
Usage: $0 COMMAND [ARG...]

Commands:
    bind [-a | --all] [-d | --device-id]
    unbind [-a | --all] [-d | --device-id]
    help [-h]
EOF
exit 0
}

# check if the PCI class of the specified GPU matchs the expected values for xdxct GPUs
# if does, return 0; otherwise, it returns 1.
is_xdxct_gpu_device() {
    local gpu=$1
    device_class_file=$(readlink -f "/sys/bus/pci/devices/$gpu/class")
    device_class=$(cat "$device_class_file")
    if [[ ${device_class} == "0x030000" ]] || [[ ${device_class} == "0x040300" ]]; then
        return 0
    fi
    return 1
}
# check if the PCI bind vfio driver
# if does, return 0; otherwise, it returns 1.
is_bound_to_vfio() {
    local gpu=$1
    local existing_driver
    local existing_driver_name
    [ -e "/sys/bus/pci/devices/$gpu/driver" ] || return 1

    existing_driver=$(readlink -f "/sys/bus/pci/devices/$gpu/driver")
    existing_driver_name=$(basename  "$existing_driver")

    [ "$existing_driver_name" == "vfio-pci" ] || return 1
    # bind to vfio
    return 0
}

unbind_driver() {
    local gpu=$1
    local existing_driver
    local existing_driver_name
    [ -e "/sys/bus/pci/devices/$gpu/driver" ] || return 0

    existing_driver=$(readlink -f "/sys/bus/pci/devices/$gpu/driver")
    existing_driver_name=$(basename  "$existing_driver")

    echo "unbinding device $gpu from driver $existing_driver_name"
    echo "$gpu" > "$existing_driver/unbind"
    echo > /sys/bus/pci/devices/$gpu/driver_override 
}

bind_device() {
    local gpu=$1
    if ! is_xdxct_gpu_device $gpu; then
        return 0
    fi
    
    if ! is_bound_to_vfio $gpu; then
        unbind_driver $gpu
        echo "start to binding vfio $gpu"
        echo "vfio-pci" > /sys/bus/pci/devices/$gpu/driver_override
        echo "$gpu" > /sys/bus/pci/drivers/vfio-pci/bind
    else
        echo "device $gpu already bound to vfio-pci"
    fi
}

bind_all() {
    for dev in /sys/bus/pci/devices/*; do
        read vendor < $dev/vendor
        if [ "$vendor" = "0x1eed" ]; then
            local device_id=$(basename $dev)
            echo $device_id
            bind_device $device_id
        fi
    done
}

handle_bind() {
    sudo modprobe vfio-pci
    if [ "$DEVICE_ID" != "" ]; then
        bind_device $DEVICE_ID
    elif [ "$ALL_DEVICES" = "true" ]; then
        bind_all
    else
        usage
    fi
}

unbind_device() {
    local gpu=$1

    if ! is_xdxct_gpu_device $gpu; then
        return 0
    fi
    echo "unbinding device $gpu"
    unbind_driver $gpu
}

unbind_all() {
    for dev in /sys/bus/pci/devices/*; do
        read vendor < $dev/vendor
        if [ "$vendor" = "0x1eed" ];then
            local device_id=$(basename $dev)
            echo $device_id
            unbind_device $device_id
        fi
    done
}

handle_unbind() {
    if [ "$DEVICE_ID" != "" ]; then
        unbind_device $DEVICE_ID
    elif [ "$ALL_DEVICES" = "true" ]; then
        unbind_all
    else
        usage
    fi
}

if [ $# -eq 0 ]; then
    usage
fi

command=$1; shift
case "${command}" in
    bind) options=$(getopt -o ad: --long all,device-id: -- "$@");;
    unbind) options=$(getopt -o ad: --long all,device-id: -- "$@");;
    help) options="" ;;
    *) usage ;;
esac

if [ $? -ne 0 ]; then
    usage
fi

eval set -- "${options}"
DEVICE_ID=""

for opt in ${options}; do
    case "$opt" in
    -a | --all) ALL_DEVICES=true; shift 1 ;;
    -d | --device-id) DEVICE_ID=$2; shift 2 ;;
    -h | --help) shift;;
    --) shift; break ;;
    esac
done

if [ $# -ne 0 ]; then
    usage
fi

if [ "$command" = "help" ]; then
    usage
elif [ "$command" = "bind" ]; then
    handle_bind
elif [ "$command" = "unbind" ]; then
    handle_unbind
else
    echo "Unknown function: $command"
    exit 1
fi

