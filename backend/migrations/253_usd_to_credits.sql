-- 计费单位由美元改为积分。
--
-- WorkBuddy 按积分结算，上游已算好余额；原先以美元命名的额度/用量字段
-- 与实际结算单位不符，容易让人把「$100 额度」误读成金额。
--
-- 只改字段名，不动数值：存量数据本来就是按上游积分记录的比例值，
-- 换算成「积分」语义即可（1 单位 = 1 积分）。
--
-- 幂等：迁移框架可能重复执行（手工预跑、环境回滚后重跑），
-- 因此逐条判断列是否存在，已改名则跳过。

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='groups' AND column_name='daily_limit_usd') THEN
    ALTER TABLE groups RENAME COLUMN daily_limit_usd TO daily_limit_credits;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='groups' AND column_name='weekly_limit_usd') THEN
    ALTER TABLE groups RENAME COLUMN weekly_limit_usd TO weekly_limit_credits;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='groups' AND column_name='monthly_limit_usd') THEN
    ALTER TABLE groups RENAME COLUMN monthly_limit_usd TO monthly_limit_credits;
  END IF;

  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='user_platform_quotas' AND column_name='daily_limit_usd') THEN
    ALTER TABLE user_platform_quotas RENAME COLUMN daily_limit_usd TO daily_limit_credits;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='user_platform_quotas' AND column_name='weekly_limit_usd') THEN
    ALTER TABLE user_platform_quotas RENAME COLUMN weekly_limit_usd TO weekly_limit_credits;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='user_platform_quotas' AND column_name='monthly_limit_usd') THEN
    ALTER TABLE user_platform_quotas RENAME COLUMN monthly_limit_usd TO monthly_limit_credits;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='user_platform_quotas' AND column_name='daily_usage_usd') THEN
    ALTER TABLE user_platform_quotas RENAME COLUMN daily_usage_usd TO daily_usage_credits;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='user_platform_quotas' AND column_name='weekly_usage_usd') THEN
    ALTER TABLE user_platform_quotas RENAME COLUMN weekly_usage_usd TO weekly_usage_credits;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='user_platform_quotas' AND column_name='monthly_usage_usd') THEN
    ALTER TABLE user_platform_quotas RENAME COLUMN monthly_usage_usd TO monthly_usage_credits;
  END IF;

  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='user_subscriptions' AND column_name='daily_usage_usd') THEN
    ALTER TABLE user_subscriptions RENAME COLUMN daily_usage_usd TO daily_usage_credits;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='user_subscriptions' AND column_name='weekly_usage_usd') THEN
    ALTER TABLE user_subscriptions RENAME COLUMN weekly_usage_usd TO weekly_usage_credits;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='user_subscriptions' AND column_name='monthly_usage_usd') THEN
    ALTER TABLE user_subscriptions RENAME COLUMN monthly_usage_usd TO monthly_usage_credits;
  END IF;

  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='billing_usage_entries' AND column_name='delta_usd') THEN
    ALTER TABLE billing_usage_entries RENAME COLUMN delta_usd TO delta_credits;
  END IF;
END $$;
