package service

// 任务 → 行为事件的映射。
//
// 事件码是上游客户端埋点的协议常量（来自两个参考实现的抓包），必须精确
// 匹配才能推进任务进度。这里按「任务代码关键词 → 事件序列」组织：活动
// 任务会增减，但同类任务用的埋点事件是稳定的。
//
// 未命中任何规则的任务不会被上报（保守策略），只走「领奖」路径——
// 这样新出现的任务最多是没效果，不会因为乱报事件把账号跑异常。

// workBuddyEventPlan 描述一个任务需要上报什么。
type workBuddyEventPlan struct {
	// Events 是事件码序列（按顺序上报）。
	Events []string
	// Repeat 是每个事件的重复次数（如「聊天 5 次」需要 5 组事件）。
	Repeat int
	// Domain 是该任务使用的设备域：资料库这类页面点击走 web。
	Domain workBuddyReportDomain
	// Fields 是事件自带的固定字段（pageURL / elementId 等）。
	// 上游按这些字段识别"点了哪里"，缺失会被判为无效事件。
	Fields map[string]any
	// NeedsRealSession 表示该任务必须带真实会话的服务端 id，
	// 纯事件上报不计数（专家/技能类）。
	NeedsRealSession bool
}

// workBuddyTaskEventPlans 是任务代码关键词到事件方案的映射。
//
// 顺序即优先级：越具体的规则越靠前。
var workBuddyTaskEventPlans = []struct {
	// match 判断任务是否命中该规则。
	match func(code string) bool
	plan  workBuddyEventPlan
}{
	// 聊天类：send + response + message 三件套，按目标次数重复。
	{match: func(c string) bool { return workBuddyContainsAny(c, "chat_5", "chat_3", "richmeow_chat") },
		plan: workBuddyEventPlan{
			Events:           []string{"chat_request_send", "chat_request_response", "chat_message_send"},
			Repeat:           5,
			Domain:           workBuddyDomainChat,
			NeedsRealSession: true, // 必须有真实会话 id
		}},
	// 桌面端对话：6 连事件链，需要真实 conversationId。
	{match: func(c string) bool { return workBuddyContainsAny(c, "desktop_chat") },
		plan: workBuddyEventPlan{
			Events:           []string{"desktop_chat_sequence"},
			Repeat:           1,
			Domain:           workBuddyDomainDesktop,
			NeedsRealSession: true,
		}},
	// 模板
	{match: func(c string) bool { return workBuddyContainsAny(c, "template") },
		plan: workBuddyEventPlan{
			Events: []string{"template_used", "agent_task_created_with_template"},
			Repeat: 5,
		}},
	// 资料库 / 文档浏览：web 域的一次页面元素点击。
	// pageURL / elementId / elementName 必须齐全，否则上游收录但不计数。
	{match: func(c string) bool { return workBuddyContainsAny(c, "library", "read") },
		plan: workBuddyEventPlan{
			Events: []string{"web_element_click"},
			Repeat: 1,
			Domain: workBuddyDomainWeb,
			Fields: map[string]any{
				"pageURL":     "https://www.workbuddy.cn/space/d/o0KWYeynteVv06UnAZqIFm",
				"elementId":   "library_doc_intro_click",
				"elementName": "WorkBuddy资料库介绍",
			},
		}},
	// 画布
	{match: func(c string) bool { return workBuddyContainsAny(c, "canvas") },
		plan: workBuddyEventPlan{
			Events: []string{"wbx_design_canvas_open", "wbx_design_canvas_task_create"},
			Repeat: 1,
		}},
	// 自动化
	{match: func(c string) bool { return workBuddyContainsAny(c, "automation", "automated") },
		plan: workBuddyEventPlan{
			Events: []string{"automated_task_create_suc", "automated_task_execute"},
			Repeat: 1,
		}},
	// 剧本
	{match: func(c string) bool { return workBuddyContainsAny(c, "playbook") },
		plan: workBuddyEventPlan{
			Events: []string{"playbook_cta_click", "playbook_prompt_send"},
			Repeat: 1,
		}},
	// 主题皮肤
	{match: func(c string) bool { return workBuddyContainsAny(c, "appearance") },
		plan: workBuddyEventPlan{Events: []string{"appearance_skin_apply"}, Repeat: 1}},
	// Buddy 协议
	{match: func(c string) bool { return workBuddyContainsAny(c, "buddy_agreement") },
		plan: workBuddyEventPlan{Events: []string{"buddy_agreement_view"}, Repeat: 1}},
	// 专家：需要真实会话的 messageId，纯上报不计数。
	{match: func(c string) bool { return workBuddyContainsAny(c, "expert") },
		plan: workBuddyEventPlan{
			Events:           []string{"expert_summon_click", "expert_summoned", "expert_actual_use"},
			Repeat:           1,
			NeedsRealSession: true,
		}},
	// 技能：同上。
	{match: func(c string) bool { return workBuddyContainsAny(c, "skill") },
		plan: workBuddyEventPlan{
			Events:           []string{"skill_info"},
			Repeat:           1,
			NeedsRealSession: true,
		}},
}

// WorkBuddyEventPlanOf 返回任务对应的事件方案。
//
// 第二个返回值为 false 表示没有可用的上报方案，调用方应跳过上报。
func WorkBuddyEventPlanOf(code string) (workBuddyEventPlan, bool) {
	for _, rule := range workBuddyTaskEventPlans {
		if rule.match(code) {
			return rule.plan, true
		}
	}
	return workBuddyEventPlan{}, false
}

// WorkBuddyBuildReportEvents 按方案展开成待上报事件序列。
func WorkBuddyBuildReportEvents(plan workBuddyEventPlan) []workBuddyReportEvent {
	if len(plan.Events) == 0 {
		return nil
	}
	repeat := plan.Repeat
	if repeat <= 0 {
		repeat = 1
	}
	out := make([]workBuddyReportEvent, 0, len(plan.Events)*repeat)
	for i := 0; i < repeat; i++ {
		for _, code := range plan.Events {
			out = append(out, workBuddyReportEvent{
				Code:   code,
				Domain: plan.Domain,
				Fields: plan.Fields,
			})
		}
	}
	return out
}
