// Copyright 2024 Hanzo Industries Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package chain

import (
	"fmt"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/errors"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	tbaas "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/tbaas/v20180416"
)

type ChainTencentChainmakerClient struct {
	ClientId     string
	ClientSecret string
	Region       string
	NetworkId    string
	ChainId      string
	Client       *tbaas.Client
}

func newChainTencentChainmakerClient(clientId, clientSecret, region, networkId, chainId string) (*ChainTencentChainmakerClient, error) {
	credential := common.NewCredential(clientId, clientSecret)
	cpf := profile.NewClientProfile()
	cpf.HttpProfile.Endpoint = "tbaas.tencentcloudapi.com"

	client, err := tbaas.NewClient(credential, region, cpf)
	if err != nil {
		return nil, fmt.Errorf("newChainTencentChainmakerClient() error: %v", err)
	}

	return &ChainTencentChainmakerClient{
		ClientId:     clientId,
		ClientSecret: clientSecret,
		Region:       region,
		NetworkId:    networkId,
		ChainId:      chainId,
		Client:       client,
	}, nil
}

func (client *ChainTencentChainmakerClient) Commit(data string) (string, string, error) {
	request := tbaas.NewInvokeRequest()
	request.Module = new("transaction")
	request.Operation = new("invoke")
	request.ClusterId = new(client.NetworkId)
	request.ChaincodeName = new("ChainMakerDemo")
	request.ChannelName = new(client.ChainId)
	request.Peers = []*tbaas.PeerSet{
		{OrgName: new("orgbeijing.chainmaker-demo"), PeerName: new("consensus1-orgbeijing.chainmaker-demo")},
	}
	request.FuncName = new("save")
	request.GroupName = new("orgbeijing.chainmaker-demo")
	request.Args = []*string{new(data)}

	response, err := client.Client.Invoke(request)
	if err != nil {
		if sdkErr, ok := err.(*errors.TencentCloudSDKError); ok {
			return "", "", fmt.Errorf("TencentCloudSDKError: %v", sdkErr)
		}

		return "", "", fmt.Errorf("ChainTencentChainmakerClient.Client.Invoke() error: %v", err)
	}

	return response.ToJsonString(), "", nil
}

func (client ChainTencentChainmakerClient) Query(blockId string, data string) (string, error) {
	return "", nil
}
