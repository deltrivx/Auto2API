package service

import "fmt"

// 批量图片功能已移除，但计费仓储仍引用这些错误与 ID 构造器。
//
// 保留最小定义：结算路径对这些符号的调用在 batch_image 移除后不会真正
// 触发（没有 job 会进入结算），但计费事务需要它们保持编译与错误语义。

// ErrBatchImageInsufficientBalance 表示批量图片任务的冻结余额不足。
var ErrBatchImageInsufficientBalance = fmt.Errorf("insufficient balance for batch image job")

// ErrBatchImageSettlementCostExceedsHold 表示结算成本超过冻结额。
var ErrBatchImageSettlementCostExceedsHold = fmt.Errorf("batch image settlement cost exceeds hold")

// BatchImageHoldRequestID 返回批量图片任务的资金冻结请求 ID。
func BatchImageHoldRequestID(batchID string) string {
	return "batch_image_hold:" + batchID
}
