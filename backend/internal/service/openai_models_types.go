package service

// OpenAIModelsResponse 是上游模型列表的响应载体。
//
// 这是 OpenAI 兼容协议的通用类型：WorkBuddy 的 /v1/models、分组模型清单、
// 账号连通性检测都通过它传递结果。
//
// 原定义还带有 upstreamETag / upstreamSourceBody / convertedFromOpenAIModelList
// 三个字段，它们只服务于 Codex 清单的 ETag 复用与格式转换。Codex 已从底层
// 移除，这三个字段没有任何读写方，因此一并删去。
type OpenAIModelsResponse struct {
	Body        []byte
	ETag        string
	NotModified bool
}

// openAIModelsRequest 描述一次上游模型列表请求。
type openAIModelsRequest struct {
	url                 string
	headers             map[string][]string
	proxyURL            string
	accountID           int64
	credentialAccountID int64
	credentialAccount   *Account
	accountConcurrency  int
	useAPIKeyUpstream   bool
	standardModelsList  bool
}

// pinnedOpenAIModelsResult 是「指定账号上的模型清单」查询结果。
type pinnedOpenAIModelsResult struct {
	account  *Account
	response *OpenAIModelsResponse
}
