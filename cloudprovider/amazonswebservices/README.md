English | [中文](./README.zh_CN.md)

For game businesses using OKG in AWS EKS clusters, routing traffic directly to Pod ports via network load balancing is the foundation for achieving high-performance real-time service discovery. Using NLB for dynamic port mapping simplifies the forwarding chain and avoids the performance loss caused by Kubernetes kube-proxy load balancing. These features are particularly crucial for handling replica combat-type game servers. For GameServerSets with the network type specified as AmazonWebServices-NLB, the AmazonWebServices-NLB network plugin will schedule an NLB, automatically allocate ports, create listeners and target groups, and associate the target group with Kubernetes services through the TargetGroupBinding CRD. If the cluster is configured with VPC-CNI, the traffic will be automatically forwarded to the Pod's IP address; otherwise, it will be forwarded through ClusterIP. The process is considered successful when the network of the GameServer is in the Ready state.

![image](./../../docs/images/aws-nlb.png)

## AmazonWebServices-NLB Configuration
### plugin Configuration
```toml
[aws]
enable = true
[aws.nlb]
# Specify the range of free ports that NLB can use to allocate external access ports for pods, with a maximum range of 50 (closed interval)
# The limit of 50 comes from AWS's limit on the number of listeners, see: https://docs.aws.amazon.com/elasticloadbalancing/latest/network/load-balancer-limits.html
max_port = 32050
min_port = 32001
```
### Preparation: ###

Due to the difference in AWS design, to achieve NLB port-to-Pod port mapping, three types of CRD resources need to be created: Listener/TargetGroup/TargetGroupBinding

#### Deploy elbv2-controller:

Definition and controller for Listener/TargetGroup CRDs: https://github.com/aws-controllers-k8s/elbv2-controller. This project links k8s resources with AWS cloud resources. Download the chart: https://gallery.ecr.aws/aws-controllers-k8s/elbv2-chart, example value.yaml:

```yaml
serviceAccount:
  annotations:
    eks.amazonaws.com/role-arn: "arn:aws:iam::xxxxxxxxx:role/test"
aws:
  region: "us-east-1"
  endpoint_url: "https://elasticloadbalancing.us-east-1.amazonaws.com"
```

The key to deploying this project lies in authorizing the k8s ServiceAccount to access the NLB SDK, which is recommended to be done through an IAM role:

##### Step 1:Enable OIDC provider for the EKS cluster

1. Sign in to the AWS Management Console.
2. Navigate to the EKS console:https://console.aws.amazon.com/eks/
3. Select your cluster.
4. On the cluster details page, ensure that the OIDC provider is enabled. Obtain the OIDC provider URL for the EKS cluster. In the "Configuration" section of the cluster details page, find the "OpenID Connect provider URL".

##### Step 2:Configure the IAM role trust policy
1. In the IAM console, create a new identity provider and select "OpenID Connect".
   - For the Provider URL, enter the OIDC provider URL of your EKS cluster.
   - For Audience, enter: `sts.amazonaws.com`

2. In the IAM console, create a new IAM role and select "Custom trust policy".
   - Use the following trust policy to allow EKS to use this role:
     ```json
     {
       "Version": "2012-10-17",
       "Statement": [
         {
           "Effect": "Allow",
           "Principal": {
             "Federated": "arn:aws:iam::<AWS_ACCOUNT_ID>:oidc-provider/oidc.eks.<REGION>.amazonaws.com/id/<OIDC_ID>"
           },
           "Action": "sts:AssumeRoleWithWebIdentity",
           "Condition": {
             "StringEquals": {
               "oidc.eks.<REGION>.amazonaws.com/id/<OIDC_ID>:sub": "system:serviceaccount:<NAMESPACE>:ack-elbv2-controller",
               "oidc.eks.<REGION>.amazonaws.com/id/<OIDC_ID>:aud": "sts.amazonaws.com"
             }
           }
         }
       ]
     }
     ```
     - Replace `<AWS_ACCOUNT_ID>`,`<REGION>`,`<OIDC_ID>`,`<NAMESPACE>` and `<SERVICE_ACCOUNT_NAME>` with your actual values.
     - Add the permission `ElasticLoadBalancingFullAccess`



#### Deploy AWS Load Balancer Controller:

CRD and controller for TargetGroupBinding: https://github.com/kubernetes-sigs/aws-load-balancer-controller/

Official deployment documentation: https://docs.aws.amazon.com/eks/latest/userguide/lbc-helm.html, essentially authorizing k8s ServiceAccount in a way similar to an IAM role.

### Parameters

#### NlbARNs
- Meaning: Fill in the ARN of the nlb, you can fill in multiple, and nlb needs to be created in AWS in advance.
- Format: Separate each nlbARN with a comma. For example: arn:aws:elasticloadbalancing:us-east-1:888888888888:loadbalancer/net/aaa/3b332e6841f23870,arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/net/bbb/5fe74944d794d27e
- Support for change: Yes

#### NlbVPCId

- Meaning: Fill in the vpcid where nlb is located, needed for creating AWS target groups.
- Format: String. For example: vpc-0bbc9f9f0ffexxxxx
- Support for change: Yes

#### NlbHealthCheck

- Meaning: Fill in the health check parameters for the nlb target group, can be left blank to use default values.
- Format: Separate each configuration with a comma. For example: "healthCheckEnabled:true,healthCheckIntervalSeconds:30,healthCheckPath:/health,healthCheckPort:8081,healthCheckProtocol:HTTP,healthCheckTimeoutSeconds:10,healthyThresholdCount:5,unhealthyThresholdCount:2"
- Support for change: Yes
- Parameter explanation:
    - **healthCheckEnabled**: Indicates whether health checks are enabled. If the target type is lambda, health checks are disabled by default but can be enabled. If the target type is instance, ip, or alb, health checks are always enabled and cannot be disabled.
    - **healthCheckIntervalSeconds**: The approximate amount of time, in seconds, between health checks of an individual target. The range is 5-300. If the target group protocol is TCP, TLS, UDP, TCP_UDP, HTTP, or HTTPS, the default is 30 seconds. If the target group protocol is GENEVE, the default is 10 seconds. If the target type is lambda, the default is 35 seconds.
    - **healthCheckPath**: The destination for health checks on the targets. For HTTP/HTTPS health checks, this is the path. For GRPC protocol version, this is the path of a custom health check method with the format /package.service/method. The default is /Amazon Web Services.ALB/healthcheck.
    - **healthCheckPort**: The port the load balancer uses when performing health checks on targets. The default is traffic-port, which is the port on which each target receives traffic from the load balancer. If the protocol is GENEVE, the default is port 80.
    - **healthCheckProtocol**: The protocol the load balancer uses when performing health checks on targets. For Application Load Balancers, the default is HTTP. For Network Load Balancers and Gateway Load Balancers, the default is TCP. The GENEVE, TLS, UDP, and TCP_UDP protocols are not supported for health checks.
    - **healthCheckTimeoutSeconds**: The amount of time, in seconds, during which no response from a target means a failed health check. The range is 2–120 seconds. For target groups with a protocol of HTTP, the default is 6 seconds. For target groups with a protocol of TCP, TLS, or HTTPS, the default is 10 seconds. For target groups with a protocol of GENEVE, the default is 5 seconds. If the target type is lambda, the default is 30 seconds.
    - **healthyThresholdCount**: The number of consecutive health check successes required before considering a target healthy. The range is 2-10. If the target group protocol is TCP, TCP_UDP, UDP, TLS, HTTP, or HTTPS, the default is 5. For target groups with a protocol of GENEVE, the default is 5. If the target type is lambda, the default is 5.
    - **unhealthyThresholdCount**: The number of consecutive health check failures required before considering a target unhealthy. The range is 2-10. If the target group protocol is TCP, TCP_UDP, UDP, TLS, HTTP, or HTTPS, the default is 2. For target groups with a protocol of GENEVE, the default is 2. If the target type is lambda, the default is 5.

#### PortProtocols
- Meaning: Ports and protocols exposed by the pod, supports specifying multiple ports/protocols.
- Format: port1/protocol1,port2/protocol2,... (protocol should be uppercase)
- Support for change: Yes

#### Fixed
- Meaning: Whether the access port is fixed. If yes, even if the pod is deleted and rebuilt, the mapping between the internal and external networks will not change.
- Format: false / true
- Support for change: Yes

#### AllowNotReadyContainers
- Meaning: The corresponding container name that allows continuous traffic during in-place upgrades.
- Format: {containerName_0},{containerName_1},... For example: sidecar
- Support for change: Not changeable during in-place upgrades

#### Annotations
- Meaning: Annotations added to the service, supports specifying multiple annotations.
- Format: key1:value1,key2:value2...
- Support for change: Yes

#### AGALabelSelector
- Meaning: Label selector that picks the target `GlobalAccelerator` CR to front this NLB. When empty, the AGA integration is disabled and the plugin behaves exactly as before. When set, the GameServer's `network-status.externalAddresses[].ip` will be filled with the AGA's static anycast IPs. See the [Global Accelerator (AGA) Integration](#global-accelerator-aga-integration) section below for details.
- Format: standard Kubernetes label selector string. For example: `game.kruise.io/aga-pool=poc-test` or `app=game,tier=prod`
- Support for change: Yes

#### AGAClientIPPreservation
- Meaning: Sets `clientIPPreservationEnabled` on the per-port endpoint that AGA forwards to. Only relevant when AGA appends a per-port listener (see [AGA Integration](#global-accelerator-aga-integration)). Note: AGA can only preserve the client IP when the NLB has a security group attached.
- Format: `true` / `false` (default: `false`)
- Support for change: Yes

#### AGANamespace
- Meaning: Namespace in which to look up the AGA CR. Defaults to the pod's namespace if unset.
- Format: String. For example: `okg-aga-poc`
- Support for change: Yes


### Usage Example
```shell
cat <<EOF | kubectl apply -f -
apiVersion: game.kruise.io/v1alpha1
kind: GameServerSet
metadata:
  name: gs-demo
  namespace: default
spec:
  replicas: 1
  updateStrategy:
    rollingUpdate:
      podUpdatePolicy: InPlaceIfPossible
  network:
    networkType: AmazonWebServices-NLB
    networkConf:
    - name: NlbARNs
      value: "arn:aws:elasticloadbalancing:us-east-1:xxxxxxxxxxxx:loadbalancer/net/okg-test/yyyyyyyyyyyyyyyy"
    - name: NlbVPCId
      value: "vpc-0bbc9f9f0ffexxxxx"
    - name: PortProtocols
      value: "80/TCP"
    - name: NlbHealthCheck
      value: "healthCheckIntervalSeconds:15"
  gameServerTemplate:
    spec:
      containers:
        - image: registry.cn-hangzhou.aliyuncs.com/gs-demo/gameserver:network
          name: gameserver
EOF
```

Check the network status of the GameServer:

```yaml
networkStatus:
    createTime: "2024-05-30T03:34:14Z"
    currentNetworkState: Ready
    desiredNetworkState: Ready
    externalAddresses:
    - endPoint: okg-test-yyyyyyyyyyyyyyyy.elb.us-east-1.amazonaws.com
      ip: ""
      ports:
      - name: "80"
        port: 32034
        protocol: TCP
    internalAddresses:
    - ip: 10.10.7.154
      ports:
      - name: "80"
        port: 80
        protocol: TCP
    lastTransitionTime: "2024-05-30T03:34:14Z"
    networkType: AmazonWebServices-NLB
```
## Global Accelerator (AGA) Integration

For latency-sensitive games and clients that can only whitelist a small fixed set of IPs, the AmazonWebServices-NLB plugin can optionally front the per-pod NLB with [AWS Global Accelerator](https://aws.amazon.com/global-accelerator/) so that players reach the GameServer through AGA's static anycast IPs and the AWS backbone. The AGA static IPs are written into the GameServer's `network-status.externalAddresses[].ip` field, which the pod can read through the standard DownwardAPI flow alongside the NLB DNS name in `endPoint`.

The AGA integration is fully opt-in. With `AGALabelSelector` empty, the AGA code path is never entered and the plugin's pure-NLB behavior is byte-for-byte unchanged.

### Preparation

The AGA integration relies on the [AWS Global Accelerator Kubernetes Controller](https://github.com/aws-controllers-k8s/globalaccelerator-controller), which provides the `GlobalAccelerator` CR (`apiVersion: aga.k8s.aws/v1alpha1`). Install it with the same IRSA pattern used for the elbv2-controller above: bind the controller's ServiceAccount to an IAM role that has the AGA management permissions (typically `GlobalAcceleratorFullAccess`) via the cluster's OIDC provider.

The OKG controller itself needs RBAC for the AGA CR (`get;list;watch;patch` on `aga.k8s.aws/globalaccelerators`); this is included automatically when you install OKG via `make deploy` from a release that ships AGA support.

### Two operating modes (auto-selected)

The plugin chooses the mode automatically per pod based on the AGA CR's existing listeners:

1. **Pre-provisioned mode (recommended for production).** The AGA already has a wide-range listener (e.g. ports `32001-32050`) pointing at the NLB. The plugin detects that the assigned port is already covered, records the assignment, and just reads the IPs back into the GameServer status -- it does **not** mutate the AGA CR. This avoids AGA's webhook rejection of overlapping port ranges for the same protocol, and is the only mode that scales beyond a few dozen GameServers per AGA.
2. **Per-port mode.** The AGA does not cover the assigned port. The plugin appends a per-port listener via a JSON Patch (`op: add, path: /spec/listeners/-`). On pod deletion, the matching listener is removed by content match (NLB ARN + exact port), not by fixed index, so concurrent pod churn cannot accidentally remove someone else's listener.

### Status propagation

The plugin starts an informer on `GlobalAccelerator` CRs. When an associated AGA gets its IPs assigned (note: AWS allocates the anycast IPs while the accelerator is still in the `IN_PROGRESS` state, not only at `DEPLOYED`), the plugin proactively reconciles all GameServers using that AGA, instead of waiting for the next pod event.

If the `aga.k8s.aws` CRD is not installed in the cluster, the AGA watcher logs a warning at startup and the rest of the NLB plugin keeps working normally.

### Usage Example (pre-provisioned mode)

The simplest setup is pre-provisioned: create one `GlobalAccelerator` CR per pool with a wide-range listener spanning the NLB's `min_port`-`max_port` range, label it, and reference it from the GameServerSet.

```shell
cat <<EOF | kubectl apply -f -
apiVersion: game.kruise.io/v1alpha1
kind: GameServerSet
metadata:
  name: gs-demo
  namespace: default
spec:
  replicas: 1
  updateStrategy:
    rollingUpdate:
      podUpdatePolicy: InPlaceIfPossible
  network:
    networkType: AmazonWebServices-NLB
    networkConf:
    - name: NlbARNs
      value: "arn:aws:elasticloadbalancing:us-east-1:xxxxxxxxxxxx:loadbalancer/net/okg-test/yyyyyyyyyyyyyyyy"
    - name: NlbVPCId
      value: "vpc-0bbc9f9f0ffexxxxx"
    - name: PortProtocols
      value: "80/TCP"
    - name: AGALabelSelector
      value: "game.kruise.io/aga-pool=poc-test"
    - name: AGANamespace
      value: "okg-aga-poc"
  gameServerTemplate:
    spec:
      containers:
        - image: registry.cn-hangzhou.aliyuncs.com/gs-demo/gameserver:network
          name: gameserver
EOF
```

The resulting GameServer status carries both the NLB DNS (in `endPoint`) and the AGA static anycast IPs (comma-separated, in `ip`):

```yaml
networkStatus:
    currentNetworkState: Ready
    desiredNetworkState: Ready
    externalAddresses:
    - endPoint: okg-test-yyyyyyyyyyyyyyyy.elb.us-east-1.amazonaws.com
      ip: "166.117.44.192,166.117.129.28"
      ports:
      - name: "80"
        port: 32034
        protocol: TCP
    networkType: AmazonWebServices-NLB
```

The pod can read these AGA IPs through DownwardAPI just like any other field of `network-status`.
