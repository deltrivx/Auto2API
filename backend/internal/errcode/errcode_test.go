package errcode

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   Class
	}{
		{403, `{"code":4100404,"message":"请求被风控拦截"}`, RiskBlocked},
		{401, `{"code":4100403,"message":"登录态过期"}`, AuthExpired},
		{403, `{"message":"unauthorized"}`, AuthExpired},
		{429, `{"message":"rate limit exceeded"}`, RateLimited},
		{400, `{"message":"invalid request"}`, BadRequest},
		{400, `{}`, BadRequest},
		{502, `bad gateway`, UpstreamError},
		{200, `ok`, Unknown},
	}
	for _, c := range cases {
		if got := Classify(c.status, c.body); got != c.want {
			t.Fatalf("Classify(%d,%q)=%s want %s", c.status, c.body, got, c.want)
		}
	}
}
