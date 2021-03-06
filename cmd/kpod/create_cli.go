package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/docker/docker/pkg/sysinfo"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

const (
	// It's not kernel limit, we want this 4M limit to supply a reasonable functional container
	linuxMinMemory = 4194304
)

func getAllLabels(labelFile, inputLabels []string) (map[string]string, error) {
	labels := make(map[string]string)
	labelErr := readKVStrings(labels, labelFile, inputLabels)
	if labelErr != nil {
		return labels, errors.Wrapf(labelErr, "unable to process labels from --label and label-file")
	}
	return labels, nil
}

func convertStringSliceToMap(strSlice []string, delimiter string) (map[string]string, error) {
	sysctl := make(map[string]string)
	for _, inputSysctl := range strSlice {
		values := strings.Split(inputSysctl, delimiter)
		if len(values) < 2 {
			return sysctl, errors.Errorf("%s in an invalid sysctl value", inputSysctl)
		}
		sysctl[values[0]] = values[1]
	}
	return sysctl, nil
}

func addWarning(warnings []string, msg string) []string {
	logrus.Warn(msg)
	return append(warnings, msg)
}

func parseVolumes(volumes []string) error {
	if len(volumes) == 0 {
		return nil
	}
	for _, volume := range volumes {
		arr := strings.SplitN(volume, ":", 3)
		if len(arr) < 2 {
			return errors.Errorf("incorrect volume format %q, should be host-dir:ctr-dir:[option]", volume)
		}
		if err := validateVolumeHostDir(arr[0]); err != nil {
			return err
		}
		if err := validateVolumeCtrDir(arr[1]); err != nil {
			return err
		}
		if len(arr) > 2 {
			if err := validateVolumeOpts(arr[2]); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateVolumeHostDir(hostDir string) error {
	if _, err := os.Stat(hostDir); err != nil {
		return errors.Wrapf(err, "error checking path %q", hostDir)
	}
	return nil
}

func validateVolumeCtrDir(ctrDir string) error {
	if ctrDir[0] != '/' {
		return errors.Errorf("invalid container directory path %q", ctrDir)
	}
	return nil
}

func validateVolumeOpts(option string) error {
	var foundRootPropagation, foundRWRO, foundLabelChange int
	options := strings.Split(option, ",")
	for _, opt := range options {
		switch opt {
		case "rw", "ro":
			if foundRWRO > 1 {
				return errors.Errorf("invalid options %q, can only specify 1 'rw' or 'ro' option", option)
			}
			foundRWRO++
		case "z", "Z":
			if foundLabelChange > 1 {
				return errors.Errorf("invalid options %q, can only specify 1 'z' or 'Z' option", option)
			}
			foundLabelChange++
		case "private", "rprivate", "shared", "rshared", "slave", "rslave":
			if foundRootPropagation > 1 {
				return errors.Errorf("invalid options %q, can only specify 1 '[r]shared', '[r]private' or '[r]slave' option", option)
			}
			foundRootPropagation++
		default:
			return errors.Errorf("invalid option type %q", option)
		}
	}
	return nil
}

func verifyContainerResources(config *createConfig, update bool) ([]string, error) {
	warnings := []string{}
	sysInfo := sysinfo.New(true)

	// memory subsystem checks and adjustments
	if config.resources.memory != 0 && config.resources.memory < linuxMinMemory {
		return warnings, fmt.Errorf("minimum memory limit allowed is 4MB")
	}
	if config.resources.memory > 0 && !sysInfo.MemoryLimit {
		warnings = addWarning(warnings, "Your kernel does not support memory limit capabilities or the cgroup is not mounted. Limitation discarded.")
		config.resources.memory = 0
		config.resources.memorySwap = -1
	}
	if config.resources.memory > 0 && config.resources.memorySwap != -1 && !sysInfo.SwapLimit {
		warnings = addWarning(warnings, "Your kernel does not support swap limit capabilities,or the cgroup is not mounted. Memory limited without swap.")
		config.resources.memorySwap = -1
	}
	if config.resources.memory > 0 && config.resources.memorySwap > 0 && config.resources.memorySwap < config.resources.memory {
		return warnings, fmt.Errorf("minimum memoryswap limit should be larger than memory limit, see usage")
	}
	if config.resources.memory == 0 && config.resources.memorySwap > 0 && !update {
		return warnings, fmt.Errorf("you should always set the Memory limit when using Memoryswap limit, see usage")
	}
	if config.resources.memorySwappiness != -1 {
		if !sysInfo.MemorySwappiness {
			msg := "Your kernel does not support memory swappiness capabilities, or the cgroup is not mounted. Memory swappiness discarded."
			warnings = addWarning(warnings, msg)
			config.resources.memorySwappiness = -1
		} else {
			swappiness := config.resources.memorySwappiness
			if swappiness < -1 || swappiness > 100 {
				return warnings, fmt.Errorf("invalid value: %v, valid memory swappiness range is 0-100", swappiness)
			}
		}
	}
	if config.resources.memoryReservation > 0 && !sysInfo.MemoryReservation {
		warnings = addWarning(warnings, "Your kernel does not support memory soft limit capabilities or the cgroup is not mounted. Limitation discarded.")
		config.resources.memoryReservation = 0
	}
	if config.resources.memoryReservation > 0 && config.resources.memoryReservation < linuxMinMemory {
		return warnings, fmt.Errorf("minimum memory reservation allowed is 4MB")
	}
	if config.resources.memory > 0 && config.resources.memoryReservation > 0 && config.resources.memory < config.resources.memoryReservation {
		return warnings, fmt.Errorf("minimum memory limit can not be less than memory reservation limit, see usage")
	}
	if config.resources.kernelMemory > 0 && !sysInfo.KernelMemory {
		warnings = addWarning(warnings, "Your kernel does not support kernel memory limit capabilities or the cgroup is not mounted. Limitation discarded.")
		config.resources.kernelMemory = 0
	}
	if config.resources.kernelMemory > 0 && config.resources.kernelMemory < linuxMinMemory {
		return warnings, fmt.Errorf("minimum kernel memory limit allowed is 4MB")
	}
	if config.resources.disableOomKiller == true && !sysInfo.OomKillDisable {
		// only produce warnings if the setting wasn't to *disable* the OOM Kill; no point
		// warning the caller if they already wanted the feature to be off
		warnings = addWarning(warnings, "Your kernel does not support OomKillDisable. OomKillDisable discarded.")
		config.resources.disableOomKiller = false
	}

	if config.resources.pidsLimit != 0 && !sysInfo.PidsLimit {
		warnings = addWarning(warnings, "Your kernel does not support pids limit capabilities or the cgroup is not mounted. PIDs limit discarded.")
		config.resources.pidsLimit = 0
	}

	if config.resources.cpuShares > 0 && !sysInfo.CPUShares {
		warnings = addWarning(warnings, "Your kernel does not support CPU shares or the cgroup is not mounted. Shares discarded.")
		config.resources.cpuShares = 0
	}
	if config.resources.cpuPeriod > 0 && !sysInfo.CPUCfsPeriod {
		warnings = addWarning(warnings, "Your kernel does not support CPU cfs period or the cgroup is not mounted. Period discarded.")
		config.resources.cpuPeriod = 0
	}
	if config.resources.cpuPeriod != 0 && (config.resources.cpuPeriod < 1000 || config.resources.cpuPeriod > 1000000) {
		return warnings, fmt.Errorf("CPU cfs period can not be less than 1ms (i.e. 1000) or larger than 1s (i.e. 1000000)")
	}
	if config.resources.cpuQuota > 0 && !sysInfo.CPUCfsQuota {
		warnings = addWarning(warnings, "Your kernel does not support CPU cfs quota or the cgroup is not mounted. Quota discarded.")
		config.resources.cpuQuota = 0
	}
	if config.resources.cpuQuota > 0 && config.resources.cpuQuota < 1000 {
		return warnings, fmt.Errorf("CPU cfs quota can not be less than 1ms (i.e. 1000)")
	}
	// cpuset subsystem checks and adjustments
	if (config.resources.cpusetCpus != "" || config.resources.cpusetMems != "") && !sysInfo.Cpuset {
		warnings = addWarning(warnings, "Your kernel does not support cpuset or the cgroup is not mounted. Cpuset discarded.")
		config.resources.cpusetCpus = ""
		config.resources.cpusetMems = ""
	}
	cpusAvailable, err := sysInfo.IsCpusetCpusAvailable(config.resources.cpusetCpus)
	if err != nil {
		return warnings, fmt.Errorf("invalid value %s for cpuset cpus", config.resources.cpusetCpus)
	}
	if !cpusAvailable {
		return warnings, fmt.Errorf("requested CPUs are not available - requested %s, available: %s", config.resources.cpusetCpus, sysInfo.Cpus)
	}
	memsAvailable, err := sysInfo.IsCpusetMemsAvailable(config.resources.cpusetMems)
	if err != nil {
		return warnings, fmt.Errorf("invalid value %s for cpuset mems", config.resources.cpusetMems)
	}
	if !memsAvailable {
		return warnings, fmt.Errorf("requested memory nodes are not available - requested %s, available: %s", config.resources.cpusetMems, sysInfo.Mems)
	}

	// blkio subsystem checks and adjustments
	if config.resources.blkioWeight > 0 && !sysInfo.BlkioWeight {
		warnings = addWarning(warnings, "Your kernel does not support Block I/O weight or the cgroup is not mounted. Weight discarded.")
		config.resources.blkioWeight = 0
	}
	if config.resources.blkioWeight > 0 && (config.resources.blkioWeight < 10 || config.resources.blkioWeight > 1000) {
		return warnings, fmt.Errorf("range of blkio weight is from 10 to 1000")
	}
	if len(config.resources.blkioWeightDevice) > 0 && !sysInfo.BlkioWeightDevice {
		warnings = addWarning(warnings, "Your kernel does not support Block I/O weight_device or the cgroup is not mounted. Weight-device discarded.")
		config.resources.blkioWeightDevice = []string{}
	}
	if len(config.resources.deviceReadBps) > 0 && !sysInfo.BlkioReadBpsDevice {
		warnings = addWarning(warnings, "Your kernel does not support BPS Block I/O read limit or the cgroup is not mounted. Block I/O BPS read limit discarded")
		config.resources.deviceReadBps = []string{}
	}
	if len(config.resources.deviceWriteBps) > 0 && !sysInfo.BlkioWriteBpsDevice {
		warnings = addWarning(warnings, "Your kernel does not support BPS Block I/O write limit or the cgroup is not mounted. Block I/O BPS write limit discarded.")
		config.resources.deviceWriteBps = []string{}
	}
	if len(config.resources.deviceReadIOps) > 0 && !sysInfo.BlkioReadIOpsDevice {
		warnings = addWarning(warnings, "Your kernel does not support IOPS Block read limit or the cgroup is not mounted. Block I/O IOPS read limit discarded.")
		config.resources.deviceReadIOps = []string{}
	}
	if len(config.resources.deviceWriteIOps) > 0 && !sysInfo.BlkioWriteIOpsDevice {
		warnings = addWarning(warnings, "Your kernel does not support IOPS Block I/O write limit or the cgroup is not mounted. Block I/O IOPS write limit discarded.")
		config.resources.deviceWriteIOps = []string{}
	}

	return warnings, nil
}
