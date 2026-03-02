# DCGM Exporter Helm Chart

This Helm chart deploys NVIDIA DCGM Exporter to monitor GPU metrics in Kubernetes clusters.

## Quick Start

```bash
# Install with default configuration
helm install dcgm-exporter ./deployment

# Install with custom values (create your own values file)
helm install dcgm-exporter ./deployment -f my-debug-values.yaml
```

## Configuration

### Debug Dump Functionality

The chart supports runtime object dumping for troubleshooting purposes. This feature allows dcgm-exporter to write debug information to files that can be analyzed later.

#### Enable Debug Dumps

```yaml
debugDump:
  enabled: true
  directory: "/tmp/dcgm-exporter-debug"  # Default location
  retention: 48  # hours (0 = no cleanup) - extended from default 24h for production use
  compression: true
```

#### Configuration Options

- `enabled`: Enable/disable debug dump functionality (default: `false`)
- `directory`: Directory to store debug dump files (default: `/tmp/dcgm-exporter-debug`)
- `retention`: Retention period in hours (default: `24`, `0` = no cleanup)
- `compression`: Use gzip compression for dump files (default: `true`)

**Note on directory choice:**
- `/tmp/dcgm-exporter-debug` (default): Temporary location, files may be lost on reboot
- `/var/log/dcgm-exporter-debug`: Persistent location, recommended for production troubleshooting

#### Persistent Storage with hostPath Volume

For production environments, you can mount the debug directory using a hostPath volume to persist logs under `/var/log/`. This ensures debug files survive pod restarts and node reboots.

The DaemonSet automatically creates a hostPath volume mount when debug dumps are enabled. Here's the relevant configuration from `deployment/templates/daemonset.yaml`:

```yaml
# Volume definition
volumes:
- name: "debug-dumps"
  hostPath:
    path: {{ .Values.debugDump.directory }}
    type: DirectoryOrCreate

# Volume mount in container
volumeMounts:
- name: "debug-dumps"
  mountPath: {{ .Values.debugDump.directory }}
```

**Example configuration for persistent storage:**

```yaml
debugDump:
  enabled: true
  directory: "/var/log/dcgm-exporter-debug"  # Persistent location
  retention: 48  # hours - extended from default 24h for production use
  compression: true
```

This configuration will:
- Create a hostPath volume at `/var/log/dcgm-exporter-debug` on each node
- Mount this directory into the container
- Persist debug files across pod restarts and node reboots
- Automatically create the directory if it doesn't exist (`DirectoryOrCreate` type)

#### Accessing Debug Files

When debug dumps are enabled, files are stored on the host filesystem at the specified directory. You can access them by:

1. **From the host node:**
   ```bash
   ls -la /tmp/dcgm-exporter-debug/
   ```

2. **From within the pod:**
   ```bash
   kubectl exec -n <namespace> <pod-name> -- ls -la /tmp/dcgm-exporter-debug/
   ```

3. **Copy files from pod:**
   ```bash
   kubectl cp <namespace>/<pod-name>:/tmp/dcgm-exporter-debug/ ./debug-files/
   ```

#### Example Usage

**Create a custom values file for debug dumps:**

Create a file named `my-debug-values.yaml` with the following content:

```yaml
debugDump:
  enabled: true
  directory: "/var/log/dcgm-exporter-debug"  # Persistent location
  retention: 48  # hours - extended from default 24h for production use
  compression: true
```

**Install with debug dumps enabled:**

```bash
# Install with custom debug configuration
helm install dcgm-exporter ./deployment -f my-debug-values.yaml

# Check if debug files are being created (adjust path based on your configuration)
kubectl exec -n dcgm-exporter <pod-name> -- ls -la /var/log/dcgm-exporter-debug/
```

**Or use default configuration:**

```bash
# Install with default debug directory (/tmp/dcgm-exporter-debug)
helm install dcgm-exporter ./deployment --set debugDump.enabled=true

# Check if debug files are being created (using default directory)
kubectl exec -n dcgm-exporter <pod-name> -- ls -la /tmp/dcgm-exporter-debug/
```

### Other Configuration Options

See `values.yaml` for all available configuration options including:
- Image configuration
- Service settings
- Resource limits
- Kubernetes integration
- TLS configuration
- Basic authentication

## Troubleshooting

### Debug Dump Files

When debug dumps are enabled, the following types of files may be created:
- Device information dumps
- Metrics dumps
- Runtime state information

These files are compressed with gzip if compression is enabled and are automatically cleaned up based on the retention period.

### Common Issues

1. **Permission denied errors**: Ensure the debug directory has appropriate permissions
2. **Disk space issues**: Monitor the debug directory size and adjust retention as needed
3. **Missing files**: Check that debug dumps are enabled and the directory is properly mounted

## Support

For issues related to DCGM Exporter, please refer to the main project documentation or create an issue in the project repository. 