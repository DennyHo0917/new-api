package router

import (
	"crypto/subtle"
	"os"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
)

// DistUserAuth checks for either New-Api-User header or standard dashboard session
func DistUserAuth() gin.HandlerFunc {
	userAuth := middleware.UserAuth()
	return func(c *gin.Context) {
		// The user header is an internal integration contract, never a public
		// identity selector. It is accepted only with a configured shared secret.
		internalSecret := strings.TrimSpace(os.Getenv("DIST_INTERNAL_SHARED_SECRET"))
		providedSecret := strings.TrimSpace(c.GetHeader("X-New-API-Internal-Secret"))
		trustedHeader := internalSecret != "" && len(internalSecret) == len(providedSecret) &&
			subtle.ConstantTimeCompare([]byte(internalSecret), []byte(providedSecret)) == 1
		if trustedHeader {
			if userIdStr := strings.TrimSpace(c.GetHeader("New-Api-User")); userIdStr != "" {
				if userId, err := strconv.Atoi(userIdStr); err == nil && userId > 0 {
					u, err := model.GetUserById(userId, false)
					if err == nil && u != nil && u.Status == common.UserStatusEnabled {
						c.Set("id", u.Id)
						c.Set("username", u.Username)
						c.Set("role", u.Role)
						c.Next()
						return
					}
				}
			}
		}

		// Fall back to the normal browser/session authentication path.
		userAuth(c)
	}
}

func SetDistRouter(router *gin.Engine) {
	distRouter := router.Group("/api/dist")
	distRouter.Use(middleware.RouteTag("dist"))
	distRouter.Use(gzip.Gzip(gzip.DefaultCompression))
	distRouter.Use(middleware.AccessTokenAudit())
	distRouter.Use(middleware.GlobalAPIRateLimit())

	// Public routes
	{
		distRouter.GET("/site/info", controller.DistGetSiteInfo)
		distRouter.GET("/site/models", controller.DistGetSiteModels)
		distRouter.GET("/site/pricing", controller.DistGetSitePricing)
		distRouter.GET("/site/packages", controller.DistGetSitePackages)
		distRouter.GET("/site/key-groups", controller.DistGetSiteKeyGroups)
		distRouter.GET("/site/key-groups/:id/pricing", controller.DistGetSiteKeyGroupPricing)
		distRouter.GET("/site/sub-distributor/info", controller.DistGetSubDistributorInfo)

		distRouter.GET("/topup/info", controller.DistGetTopupInfo)
		distRouter.POST("/topup/amount", controller.DistCalculateAmount)

		distRouter.POST("/user/register", controller.Register)
		distRouter.POST("/user/login", controller.DistLogin)
		distRouter.POST("/user/logout", controller.DistLogout)
	}

	// Authenticated routes
	authGroup := distRouter.Group("")
	authGroup.Use(DistUserAuth())
	{
		authGroup.GET("/user/self", controller.DistGetUserSelf)
		authGroup.PUT("/user/password", controller.DistUpdateUserPassword)
		authGroup.GET("/user/usage", controller.DistGetUserUsage)
		authGroup.GET("/user/logs", controller.GetUserLogs)
		authGroup.GET("/user/logs/stat", controller.GetLogsSelfStat)
		authGroup.GET("/user/tasks", controller.DistUserTasks)
		authGroup.GET("/user/mj", controller.DistUserMj)

		authGroup.GET("/token/list", controller.DistGetTokens)
		authGroup.POST("/token/create", controller.DistCreateToken)
		authGroup.PUT("/token/:id", controller.DistUpdateToken)
		authGroup.DELETE("/token/:id", controller.DistDeleteToken)
		authGroup.GET("/token/:id/models", controller.DistGetTokenModels)

		authGroup.POST("/topup/crypto/pay", controller.CreateCryptoOrder)
		authGroup.POST("/topup/crypto/submit", controller.SubmitCryptoTxHash)
		authGroup.GET("/topup/crypto/status", controller.GetCryptoOrderStatus)
		authGroup.POST("/topup/redeem", controller.DistRedeemCode)
		authGroup.POST("/topup/pay", controller.RequestEpay)
		authGroup.POST("/topup/stripe/pay", controller.RequestStripePay)
		authGroup.POST("/topup/stripe/amount", controller.RequestStripeAmount)
		authGroup.GET("/topup/history", controller.DistGetTopupHistory)

		authGroup.POST("/package/subscribe", controller.DistSubscribePackage)
		authGroup.GET("/package/subscriptions", controller.DistGetActiveSubscriptions)

		authGroup.GET("/aff", controller.DistGetAffCode)
		authGroup.POST("/aff_transfer", controller.DistAffTransfer)
		authGroup.GET("/aff_earnings", controller.DistAffEarnings)
		authGroup.GET("/aff_payouts", controller.DistAffPayouts)
		authGroup.POST("/aff_withdraw", controller.DistAffWithdraw)
		authGroup.POST("/kol_apply", controller.DistKolApply)
		authGroup.GET("/kol_status", controller.DistKolStatus)

		authGroup.GET("/invoice/info", controller.DistInvoiceInfo)
		authGroup.GET("/invoice/history", controller.DistInvoiceHistory)
		authGroup.POST("/invoice", controller.DistCreateInvoice)
	}
}
