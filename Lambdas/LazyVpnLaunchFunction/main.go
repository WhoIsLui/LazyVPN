package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2Types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3Types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type Event struct {
	InstanceType string `json:"instanceType"`
	Region       string `json:"region"`
	PublicIpV4   string `json:"publicIpV4"`
	PublicIpV6   string `json:"publicIpV6"`
}

type RegionConfig struct {
	AmiID            string
	AvailabilityZone string
}

// Ubuntu AMI IDs and Availability Zones
var mapRegionConfig = map[string]RegionConfig{
	"sa-east-1": {"ami-0b6c2d49148000cd5", "sa-east-1c"},
	// "us-east-1":      {"ami-0fc5d935ebf8bc3bc", "us-east-1c"},
	"us-east-2": {"ami-0e83be366243f524a", "us-east-2c"},
	"us-west-1": {"ami-0cbd40f694b804622", "us-west-1c"},
	// "us-west-2":      {"ami-0efcece6bed30fd98", "us-west-2c"},
	"ap-south-1": {"ami-0287a05f0ef0e9d9a", "ap-south-1c"},
	// "ap-northeast-3": {"ami-035322b237ca6d47a", "ap-northeast-3c"},
	"ap-northeast-2": {"ami-086cae3329a3f7d75", "ap-northeast-2c"},
	// "ap-southeast-1": {"ami-078c1149d8ad719a7", "ap-southeast-1c"},
	"ap-southeast-2": {"ami-0df4b2961410d4cff", "ap-southeast-2c"},
	// "ap-northeast-1": {"ami-09a81b370b76de6a2", "ap-northeast-1c"},
	"ca-central-1": {"ami-06873c81b882339ac", "ca-central-1c"},
	"eu-central-1": {"ami-06dd92ecc74fdfb36", "eu-central-1c"},
	// "eu-west-1":      {"ami-0694d931cee176e7d", "eu-west-1c"},
	"eu-west-2": {"ami-0505148b3591e4c07", "eu-west-2c"},
	// "eu-west-3":      {"ami-00983e8a26e4c9bd9", "eu-west-3c"},
	"eu-north-1": {"ami-0fe8bec493a81c7da", "eu-north-1c"},
}

func HandleRequest(ctx context.Context, event *Event) (*string, error) {
	if event == nil {
		return nil, fmt.Errorf("received nil event")
	}

	if mapRegionConfig[event.Region].AmiID == "" {
		return nil, fmt.Errorf("region not supported")
	}
	amiId, avbZone := mapRegionConfig[event.Region].AmiID, mapRegionConfig[event.Region].AvailabilityZone

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(event.Region))
	if err != nil {
		log.Fatalf("Unable to load SDK config, %v", err)
	}

	ec2Client := ec2.NewFromConfig(cfg, func(o *ec2.Options) {
		o.Region = event.Region
	})

	vpcID, ipV6CidrBlock, err := CreateAwsVpc(ec2Client)
	if err != nil {
		fmt.Println("Error creating vpc", err)
		return nil, err
	}

	subnetID, err := CreateSN(ec2Client, &vpcID, &ipV6CidrBlock, avbZone)
	if err != nil {
		fmt.Println("Error creating subnet", err)
		return nil, err
	}

	_, err = CreateIgwAndRouteTable(ec2Client, &vpcID, &subnetID)
	if err != nil {
		fmt.Println("Error creating igw and route table", err)
		return nil, err
	}

	sgID, err := CreateSG(ec2Client, &vpcID)
	if err != nil {
		fmt.Println("Error creating security group", err)
		return nil, err
	}

	ec2Response, err := CreateEc2Instance(ec2Client, event, &vpcID, &subnetID, &sgID, amiId, avbZone) //TODO: Change ami according to region
	if err != nil {
		fmt.Println("Error creating instance")
		return nil, err
	}

	err = CreateS3Bucket(ctx)
	if err != nil {
		fmt.Println("Error creating bucket")
		return nil, err
	}

	message := fmt.Sprintf("Ec2 %v created in %s region!", ec2Response, event.Region)
	return &message, nil
}

func GetUserDataBase64() string {
	fileContents := `#!/bin/bash
	apt-get update
	apt-get install awscli -y
	
	curl -O https://raw.githubusercontent.com/angristan/openvpn-install/master/openvpn-install.sh
	chmod +x openvpn-install.sh
	APPROVE_INSTALL=y ENDPOINT=$(curl -4 ifconfig.co) APPROVE_IP=y IPV6_SUPPORT=y PORT_CHOICE=1 PROTOCOL_CHOICE=1 DNS=1 COMPRESSION_ENABLED=n  CUSTOMIZE_ENC=n CLIENT=openvpn PASS=1 ./openvpn-install.sh

	mv /root/openvpn.ovpn /tmp/
	chmod 777 /tmp/openvpn.ovpn
	export PUBLIC_IP=$(curl -s https://api.ipify.org)
	sed "4s#.*#remote $PUBLIC_IP#" /tmp/openvpn.ovpn -i
	aws s3 cp /tmp/openvpn.ovpn s3://lazy-vpn-art/
	`

	base64Enc := base64.StdEncoding.EncodeToString([]byte(fileContents))
	return base64Enc
}

func ControlUserAccess(ec2Client *ec2.Client, securityGroupId *string, publicIpV4 string, publicIpV6 string) error {
	_, err := ec2Client.AuthorizeSecurityGroupIngress(context.TODO(), &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: securityGroupId,
		IpPermissions: []ec2Types.IpPermission{
			{
				IpProtocol: aws.String("tcp"),
				FromPort:   aws.Int32(22),
				ToPort:     aws.Int32(22),
				IpRanges: []ec2Types.IpRange{
					{
						CidrIp:      aws.String(publicIpV4 + "/32"),
						Description: aws.String("SSH access from my IP"),
					},
				},
			},
			{
				IpProtocol: aws.String("udp"),
				FromPort:   aws.Int32(1194),
				ToPort:     aws.Int32(1194),
				IpRanges: []ec2Types.IpRange{
					{
						CidrIp:      aws.String(publicIpV4 + "/32"),
						Description: aws.String("HTTPS access"),
					},
				},
			},
			{
				IpProtocol: aws.String("-1"),
				Ipv6Ranges: []ec2Types.Ipv6Range{
					{
						CidrIpv6: aws.String(publicIpV6 + "/128"),
					},
				},
			},
		},
	})

	return err
}

func CreateAwsVpc(ec2Client *ec2.Client) (string, string, error) {
	vpc, err := ec2Client.CreateVpc(context.TODO(), &ec2.CreateVpcInput{
		CidrBlock:                   aws.String("10.0.0.0/28"),
		AmazonProvidedIpv6CidrBlock: aws.Bool(false),
	})
	if err != nil {
		fmt.Println("Error creating vpc", err)
		return "", "", err
	}

	_, err = ec2Client.AssociateVpcCidrBlock(context.TODO(), &ec2.AssociateVpcCidrBlockInput{
		VpcId:                       vpc.Vpc.VpcId,
		AmazonProvidedIpv6CidrBlock: aws.Bool(true),
	})
	if err != nil {
		fmt.Println("Error associating vpc cidr block", err)
		return "", "", err
	}

	for {
		vpcResponse, err := ec2Client.DescribeVpcs(context.TODO(), &ec2.DescribeVpcsInput{
			VpcIds: []string{*vpc.Vpc.VpcId},
		})
		if err != nil {
			fmt.Println("Error describing vpc", err)
			return "", "", err
		}

		if len(vpcResponse.Vpcs[0].Ipv6CidrBlockAssociationSet) > 0 {
			ipV6CidrBlock := vpcResponse.Vpcs[0].Ipv6CidrBlockAssociationSet[0].Ipv6CidrBlock
			return *vpc.Vpc.VpcId, *ipV6CidrBlock, nil
		}

		// Wait for a few seconds before checking again
		time.Sleep(5 * time.Second)
	}

}

func CreateSN(ec2Client *ec2.Client, vpcID *string, ipV6CidrBlock *string, avbZone string) (string, error) {
	subnet, err := ec2Client.CreateSubnet(context.TODO(), &ec2.CreateSubnetInput{
		VpcId:            vpcID,
		CidrBlock:        aws.String("10.0.0.0/28"),
		Ipv6CidrBlock:    ipV6CidrBlock,
		AvailabilityZone: aws.String(avbZone),
	})

	if err != nil {
		fmt.Println("Error creating subnet", err)
		return "", err
	}

	_, err = ec2Client.ModifySubnetAttribute(context.TODO(), &ec2.ModifySubnetAttributeInput{
		SubnetId: subnet.Subnet.SubnetId,
		AssignIpv6AddressOnCreation: &ec2Types.AttributeBooleanValue{
			Value: aws.Bool(true),
		},
	})

	if err != nil {
		fmt.Println("Error modifying subnet attribute", err)
		return "", err
	}

	return *subnet.Subnet.SubnetId, nil
}

func CreateIgwAndRouteTable(ec2Client *ec2.Client, vpcID *string, subnetID *string) (string, error) {
	igw, err := ec2Client.CreateInternetGateway(context.TODO(), &ec2.CreateInternetGatewayInput{})
	if err != nil {
		fmt.Println("Error creating internet gateway", err)
		return "", err
	}

	_, err = ec2Client.AttachInternetGateway(context.TODO(), &ec2.AttachInternetGatewayInput{
		InternetGatewayId: igw.InternetGateway.InternetGatewayId,
		VpcId:             vpcID,
	})
	if err != nil {
		fmt.Println("Error attaching internet gateway to vpc", err)
		return "", err
	}

	routeTable, err := ec2Client.CreateRouteTable(context.TODO(), &ec2.CreateRouteTableInput{
		VpcId: vpcID,
	})
	if err != nil {
		fmt.Println("Error creating route table", err)
		return "", err
	}

	_, err = ec2Client.CreateRoute(context.TODO(), &ec2.CreateRouteInput{
		RouteTableId:         routeTable.RouteTable.RouteTableId,
		DestinationCidrBlock: aws.String("0.0.0.0/0"),
		GatewayId:            igw.InternetGateway.InternetGatewayId,
	})
	if err != nil {
		fmt.Println("Error creating route to internet gateway", err)
		return "", err
	}

	_, err = ec2Client.CreateRoute(context.TODO(), &ec2.CreateRouteInput{
		RouteTableId:             routeTable.RouteTable.RouteTableId,
		DestinationIpv6CidrBlock: aws.String("::/0"),
		GatewayId:                igw.InternetGateway.InternetGatewayId,
	})
	if err != nil {
		fmt.Println("Error creating IPv6 route to internet gateway", err)
		return "", err
	}

	_, err = ec2Client.AssociateRouteTable(context.TODO(), &ec2.AssociateRouteTableInput{
		SubnetId:     subnetID,
		RouteTableId: routeTable.RouteTable.RouteTableId,
	})
	if err != nil {
		fmt.Println("Error associating route table with subnet", err)
		return "", err
	}

	return "", nil
}

func CreateSG(ec2Client *ec2.Client, vpcID *string) (string, error) {
	securityGroupID, err := ec2Client.CreateSecurityGroup(context.TODO(), &ec2.CreateSecurityGroupInput{
		VpcId:       vpcID,
		GroupName:   aws.String("SSH-ONLY"),
		Description: aws.String("Security group for SSH access"),
	})
	if err != nil {
		fmt.Println("Error creating security group", err)
		return "", err
	}

	return *securityGroupID.GroupId, nil
}

func CreateEc2Instance(ec2Client *ec2.Client, event *Event, vpcID *string, subnetID *string, sgID *string, amiID string, avbZone string) (string, error) {

	err := ControlUserAccess(ec2Client, sgID, event.PublicIpV4, event.PublicIpV6)
	if err != nil {
		fmt.Println("Error controlling user access", err)
		return "", err
	}

	userData := GetUserDataBase64()
	instanceType := ec2Types.InstanceType(event.InstanceType)

	runResult, err := ec2Client.RunInstances(context.TODO(), &ec2.RunInstancesInput{
		ImageId:      aws.String(amiID),
		InstanceType: instanceType,
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		KeyName:      aws.String("LazyVPN"),
		IamInstanceProfile: &ec2Types.IamInstanceProfileSpecification{
			Name: aws.String("LazyVpnEc2Role"),
		},
		NetworkInterfaces: []ec2Types.InstanceNetworkInterfaceSpecification{
			{
				DeviceIndex:              aws.Int32(0),
				DeleteOnTermination:      aws.Bool(true),
				AssociatePublicIpAddress: aws.Bool(true),
				SubnetId:                 subnetID,
				Groups:                   []string{*sgID},
			},
		},
		Placement: &ec2Types.Placement{
			AvailabilityZone: aws.String(avbZone),
		},
		UserData: aws.String(userData),
		MetadataOptions: &ec2Types.InstanceMetadataOptionsRequest{
			HttpTokens: ec2Types.HttpTokensStateRequired,
		},
	})

	if err != nil {
		fmt.Println("Error creating instance", err)
		ec2Client.DeleteVpc(context.TODO(), &ec2.DeleteVpcInput{VpcId: vpcID})
		ec2Client.DeleteSecurityGroup(context.TODO(), &ec2.DeleteSecurityGroupInput{GroupId: sgID})
		return "", err
	}

	fmt.Println("Created instance", *runResult.Instances[0].InstanceId)
	return *runResult.Instances[0].InstanceId, nil
}

func CreateS3Bucket(ctx context.Context) error {

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("sa-east-1"))
	if err != nil {
		log.Fatalf("Unable to load SDK config, %v", err)
	}

	s3Client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.Region = "sa-east-1"
	})

	_, err = s3Client.CreateBucket(context.TODO(), &s3.CreateBucketInput{
		Bucket: aws.String("lazy-vpn-art"),
		CreateBucketConfiguration: &s3Types.CreateBucketConfiguration{
			LocationConstraint: s3Types.BucketLocationConstraintSaEast1,
		},
	})

	return err
}

func main() {
	lambda.Start(HandleRequest)
}
