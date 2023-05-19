package api

import (
	"context"
	"database/sql"
	. "delta/api/models"
	"delta/core"
	"delta/jobs"
	"delta/utils"
	"encoding/json"
	"fmt"
	model "github.com/application-research/delta-db/db_models"
	"github.com/google/uuid"
	"github.com/ipfs/go-cid"
	"github.com/multiformats/go-multiaddr"
	"github.com/pkg/errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"gorm.io/gorm"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

var statsService *core.StatsService

// ConfigureDealRouter It's a function that takes a pointer to an echo.Group and a pointer
// to a DeltaNode, and then it adds a bunch of routes to the echo.Group
func ConfigureDealRouter(e *echo.Group, node *core.DeltaNode) {

	statsService = core.NewStatsStatsService(node)
	dealMake := e.Group("/deal")

	// upload limiter middleware
	dealMake.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return checkMetaFlags(next, node)
	})

	dealPrepare := dealMake.Group("/prepare")
	dealAnnounce := dealMake.Group("/announce")
	dealStatus := dealMake.Group("/status")

	dealMake.POST("/end-to-end", func(c echo.Context) error {
		return handleEndToEndDeal(c, node)
	})

	dealMake.POST("/import", func(c echo.Context) error {
		return handleImportDeal(c, node)
	})

	dealMake.POST("/imports", func(c echo.Context) error {
		return handleMultipleImportDeals(c, node)
	})

	dealMake.POST("/batch/imports", func(c echo.Context) error {
		return handleMultipleBatchImportDeals(c, node)
	})

	dealPrepare.POST("/content", func(c echo.Context) error {
		// TODO: call prepare unsigned deal proposal
		return nil
	})

	dealPrepare.POST("/piece-commitment", func(c echo.Context) error {
		// TODO: call prepare unsigned deal proposal with piece commitment
		return nil
	})

	dealPrepare.POST("/piece-commitments", func(c echo.Context) error {
		// TODO: call prepare unsigned deal proposal with piece commitments
		return nil
	})

	dealAnnounce.POST("/content", func(c echo.Context) error {
		// TODO: accept a hexed signed proposal
		return nil
	})

	dealAnnounce.POST("/piece-commitment", func(c echo.Context) error {
		return nil
	})

	dealAnnounce.POST("/piece-commitments", func(c echo.Context) error {
		return nil
	})

	dealStatus.POST("/content/:contentId", func(c echo.Context) error {
		return handleContentStats(c, *statsService)
	})
	dealStatus.POST("/piece-commitment/:piece-commitmentId", func(c echo.Context) error {
		return handleCommitmentPieceStats(c, *statsService)

	})
}

// checkMetaFlags checks if the request is disabled or if the node is in maintenance mode
// This function takes in a handler function and a DeltaNode object and returns a function that checks for meta flags in
// the context before calling the handler.
func checkMetaFlags(next echo.HandlerFunc, node *core.DeltaNode) func(c echo.Context) error {
	return func(c echo.Context) error {

		_, span := otel.Tracer("handleNodePeers").Start(context.Background(), "handleNodeHostApiKey")
		defer span.End()

		span.SetName("checkMetaFlags")
		span.SetAttributes(attribute.String("user-agent", c.Request().UserAgent()))
		span.SetAttributes(attribute.String("path", c.Path()))
		span.SetAttributes(attribute.String("method", c.Request().Method))
		span.SetAttributes(attribute.String("remote_ip", c.RealIP()))
		span.SetAttributes(attribute.String("host", c.Request().Host))
		span.SetAttributes(attribute.String("referer", c.Request().Referer()))
		span.SetAttributes(attribute.String("request_uri", c.Request().RequestURI))

		// check if the sum(size) transfer-started and created_at within instance_start time
		var meta model.InstanceMeta
		node.DB.First(&meta)

		if meta.DisableRequest {
			return c.JSON(http.StatusForbidden, "request is disabled")
		}
		return next(c)
	}
}

// checkResourceLimits checks if the sum of the size of all the files that are currently being transferred is greater
// It checks if the sum of the size of all the files that are currently being transferred is greater than the number of
// CPUs multiplied by the number of bytes per CPU. If it is, then it returns an error
func checkResourceLimits(next echo.HandlerFunc, node *core.DeltaNode) func(c echo.Context) error {
	return func(c echo.Context) error {

		_, span := otel.Tracer("handleNodePeers").Start(context.Background(), "handleNodeHostApiKey")
		defer span.End()

		span.SetName("checkMetaFlags")
		span.SetAttributes(attribute.String("user-agent", c.Request().UserAgent()))
		span.SetAttributes(attribute.String("path", c.Path()))
		span.SetAttributes(attribute.String("method", c.Request().Method))
		span.SetAttributes(attribute.String("remote_ip", c.RealIP()))
		span.SetAttributes(attribute.String("host", c.Request().Host))
		span.SetAttributes(attribute.String("referer", c.Request().Referer()))
		span.SetAttributes(attribute.String("request_uri", c.Request().RequestURI))

		var size sql.NullInt64
		node.DB.Raw("select sum(size) from contents where status = 'transfer-started' and created_at > ?", node.MetaInfo.InstanceStart).Scan(&size)

		// memory limit (10GB per CPU)
		if size != (sql.NullInt64{}) {
			if uint64(size.Int64) > (node.MetaInfo.NumberOfCpus * node.MetaInfo.BytesPerCpu) {
				return c.JSON(http.StatusForbidden, DealResponse{
					Status:  "error",
					Message: "Too much data is being transferred, please try again once all other transfers are complete",
				})
			}
		}
		return next(c)
	}
}

// handleExistingContentsAdd handles the request to add existing content to the network
// @Summary Add existing content to the network
// @Description Add existing content to the network
// @Tags deal
// @Accept  json
// @Produce  json
func handleExistingContentsAdd(c echo.Context, node *core.DeltaNode) error {
	var dealRequests []DealRequest

	// lets record this.
	authorizationString := c.Request().Header.Get("Authorization")
	authParts := strings.Split(authorizationString, " ")

	//	validate the meta
	err := c.Bind(&dealRequests)

	if err != nil {
		return errors.New("Error parsing the request, please check the request body if it complies with the spec")
	}

	errTxn := node.DB.Transaction(func(tx *gorm.DB) error {
		var dealResponses []DealResponse
		for _, dealRequest := range dealRequests {
			err = ValidateMeta(dealRequest, node)
			if err != nil {
				// return the error from the validation
				return err
			}

			decodeCid, err := cid.Decode(dealRequest.Cid)
			if err != nil {
				return errors.New("Error decoding the cid")
			}

			addNode, err := node.Node.Get(context.Background(), decodeCid)
			if err != nil {
				return errors.New("Error pinning the file")
			}

			// specify the connection mode
			var connMode = dealRequest.ConnectionMode
			if connMode == "" || (connMode != utils.CONNECTION_MODE_E2E && connMode != utils.CONNECTION_MODE_IMPORT) {
				connMode = "e2e"
			}

			// let's create a commp but only if we have
			// a cid, a piece_cid, a padded_piece_size, size
			var pieceCommp model.PieceCommitment
			if (PieceCommitmentRequest{} != dealRequest.PieceCommitment && dealRequest.PieceCommitment.Piece != "") &&
				(dealRequest.PieceCommitment.PaddedPieceSize != 0) &&
				(dealRequest.Size != 0) {

				// if commp is there, make sure the piece and size are there. Use default duration.
				pieceCommp.Cid = addNode.Cid().String()
				pieceCommp.Piece = dealRequest.PieceCommitment.Piece
				pieceCommp.Size = dealRequest.Size
				pieceCommp.UnPaddedPieceSize = dealRequest.PieceCommitment.UnPaddedPieceSize
				pieceCommp.PaddedPieceSize = dealRequest.PieceCommitment.PaddedPieceSize
				pieceCommp.CreatedAt = time.Now()
				pieceCommp.UpdatedAt = time.Now()
				pieceCommp.Status = utils.COMMP_STATUS_OPEN
				tx.Create(&pieceCommp)
				dealRequest.PieceCommitment = PieceCommitmentRequest{
					Piece:             pieceCommp.Piece,
					PaddedPieceSize:   pieceCommp.PaddedPieceSize,
					UnPaddedPieceSize: pieceCommp.UnPaddedPieceSize,
				}
			}

			// save the content to the DB with the piece_commitment_id
			cidName := addNode.Cid().String()
			cidSize, err := addNode.Size()
			if err != nil {
				return errors.New("Error getting the size of the cid")
			}
			content := model.Content{
				Name:              cidName,
				Size:              int64(cidSize),
				Cid:               addNode.Cid().String(),
				RequestingApiKey:  authParts[1],
				PieceCommitmentId: pieceCommp.ID,
				AutoRetry:         dealRequest.AutoRetry,
				Status:            utils.CONTENT_PINNED,
				ConnectionMode:    connMode,
				CreatedAt:         time.Now(),
				UpdatedAt:         time.Now(),
			}
			tx.Create(&content)
			dealRequest.Cid = content.Cid

			//	assign a miner
			if dealRequest.Miner != "" {
				contentMinerAssignment := model.ContentMiner{
					Miner:     dealRequest.Miner,
					Content:   content.ID,
					CreatedAt: time.Now(),
					UpdatedAt: time.Now(),
				}
				node.DB.Create(&contentMinerAssignment)
				dealRequest.Miner = contentMinerAssignment.Miner
			}

			if (WalletRequest{} != dealRequest.Wallet) {

				// get wallet from wallets database
				var wallet model.Wallet

				if dealRequest.Wallet.Address != "" {
					tx.Where("addr = ? and owner = ?", dealRequest.Wallet.Address, authParts[1]).First(&wallet)
				} else if dealRequest.Wallet.Uuid != "" {
					tx.Where("uuid = ? and owner = ?", dealRequest.Wallet.Uuid, authParts[1]).First(&wallet)
				} else {
					tx.Where("id = ? and owner = ?", dealRequest.Wallet.Id, authParts[1]).First(&wallet)
				}

				if wallet.ID == 0 {
					return errors.New("Wallet not found, please make sure the wallet is registered")
				}

				// create the wallet request object
				var hexedWallet WalletRequest
				hexedWallet.KeyType = wallet.KeyType
				hexedWallet.PrivateKey = wallet.PrivateKey

				if err != nil {
					return errors.New("Error encoding the wallet")
				}

				// assign the wallet to the content
				contentWalletAssignment := model.ContentWallet{
					WalletId:  wallet.ID,
					Content:   content.ID,
					CreatedAt: time.Now(),
					UpdatedAt: time.Now(),
				}
				node.DB.Create(&contentWalletAssignment)

				dealRequest.Wallet = WalletRequest{
					Id:      dealRequest.Wallet.Id,
					Address: wallet.Addr,
				}
			}

			var dealProposalParam model.ContentDealProposalParameters
			dealProposalParam.CreatedAt = time.Now()
			dealProposalParam.UpdatedAt = time.Now()
			dealProposalParam.Content = content.ID
			dealProposalParam.UnverifiedDealMaxPrice = func() string {
				if dealRequest.UnverifiedDealMaxPrice != "" {
					return dealRequest.UnverifiedDealMaxPrice
				}
				return "0"
			}()

			dealProposalParam.Label = func() string {
				if dealRequest.Label != "" {
					return dealRequest.Label
				}
				return content.Cid
			}()

			dealProposalParam.VerifiedDeal = func() bool {
				if dealRequest.DealVerifyState == utils.DEAL_VERIFIED {
					return true
				}
				return false
			}()

			if dealRequest.StartEpochInDays != 0 && dealRequest.DurationInDays != 0 {
				startEpochTime := time.Now().AddDate(0, 0, int(dealRequest.StartEpochInDays))
				dealProposalParam.StartEpoch = utils.DateToHeight(startEpochTime)
				dealProposalParam.EndEpoch = dealProposalParam.StartEpoch + (utils.EPOCH_PER_DAY * (dealRequest.DurationInDays))
				dealProposalParam.Duration = dealProposalParam.EndEpoch - dealProposalParam.StartEpoch
			}

			dealProposalParam.RemoveUnsealedCopy = dealRequest.RemoveUnsealedCopy
			dealProposalParam.SkipIPNIAnnounce = dealRequest.SkipIPNIAnnounce

			// deal proposal parameters
			tx.Create(&dealProposalParam)

			if err != nil {
				return errors.New("Error pinning the file")
			}

			var dispatchJobs core.IProcessor
			if pieceCommp.ID != 0 {
				dispatchJobs = jobs.NewStorageDealMakerProcessor(node, content, pieceCommp) // straight to storage deal making
			} else {
				dispatchJobs = jobs.NewPieceCommpProcessor(node, content) // straight to pieceCommp
			}

			node.Dispatcher.AddJob(dispatchJobs)

			dealResponses = append(dealResponses, DealResponse{
				Status:                       "success",
				Message:                      "Deal request received. Please take note and check the status of the deal using the content_id.",
				ContentId:                    content.ID,
				DealRequest:                  dealRequest,
				DealProposalParameterRequest: dealProposalParam,
			})
		}
		node.Dispatcher.Start(len(dealRequests))
		return c.JSON(http.StatusOK, dealResponses)
	})
	if errTxn != nil {
		return errors.New("Error creating the transaction")
	}
	return nil
}

// handleExistingContentAdd handles the request to add content to the network
// @Summary Add content to the network
// @Description Add content to the network
// @Tags deal
// @Accept  json
// @Produce  json
func handleExistingContentAdd(c echo.Context, node *core.DeltaNode) error {

	// TODO: this needs a source
	var dealRequest DealRequest

	// lets record this.
	authorizationString := c.Request().Header.Get("Authorization")
	authParts := strings.Split(authorizationString, " ")
	err := c.Bind(&dealRequest)
	err = ValidateMeta(dealRequest, node)
	if err != nil {
		// return the error from the validation
		return err
	}

	decodeCid, err := cid.Decode(dealRequest.Cid)
	if err != nil {
		return errors.New("Error decoding the cid")
	}

	addNode, err := node.Node.Get(context.Background(), decodeCid)
	if err != nil {
		return errors.New("Error pinning the file")
	}

	// specify the connection mode
	var connMode = dealRequest.ConnectionMode
	if connMode == "" || (connMode != utils.CONNECTION_MODE_E2E && connMode != utils.CONNECTION_MODE_IMPORT) {
		connMode = "e2e"
	}

	errTxn := node.DB.Transaction(func(tx *gorm.DB) error {
		// let's create a commp but only if we have
		// a cid, a piece_cid, a padded_piece_size, size
		var pieceCommp model.PieceCommitment
		if (PieceCommitmentRequest{} != dealRequest.PieceCommitment && dealRequest.PieceCommitment.Piece != "") &&
			(dealRequest.PieceCommitment.PaddedPieceSize != 0) &&
			(dealRequest.Size != 0) {

			// if commp is there, make sure the piece and size are there. Use default duration.
			pieceCommp.Cid = addNode.Cid().String()
			pieceCommp.Piece = dealRequest.PieceCommitment.Piece
			pieceCommp.Size = dealRequest.Size
			pieceCommp.UnPaddedPieceSize = dealRequest.PieceCommitment.UnPaddedPieceSize
			pieceCommp.PaddedPieceSize = dealRequest.PieceCommitment.PaddedPieceSize
			pieceCommp.CreatedAt = time.Now()
			pieceCommp.UpdatedAt = time.Now()
			pieceCommp.Status = utils.COMMP_STATUS_OPEN
			node.DB.Create(&pieceCommp)

			dealRequest.PieceCommitment = PieceCommitmentRequest{
				Piece:             pieceCommp.Piece,
				PaddedPieceSize:   pieceCommp.PaddedPieceSize,
				UnPaddedPieceSize: pieceCommp.UnPaddedPieceSize,
			}
		}

		// save the content to the DB with the piece_commitment_id
		cidName := addNode.Cid().String()
		cidSize, err := addNode.Size()
		if err != nil {
			return errors.New("Error getting the size of the cid")
		}
		content := model.Content{
			Name:              cidName,
			Size:              int64(cidSize),
			Cid:               addNode.Cid().String(),
			RequestingApiKey:  authParts[1],
			PieceCommitmentId: pieceCommp.ID,
			AutoRetry:         dealRequest.AutoRetry,
			Status:            utils.CONTENT_PINNED,
			ConnectionMode:    connMode,
			CreatedAt:         time.Now(),
			UpdatedAt:         time.Now(),
		}
		node.DB.Create(&content)
		dealRequest.Cid = content.Cid

		//	assign a miner
		if dealRequest.Miner != "" {
			contentMinerAssignment := model.ContentMiner{
				Miner:     dealRequest.Miner,
				Content:   content.ID,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			node.DB.Create(&contentMinerAssignment)
			dealRequest.Miner = contentMinerAssignment.Miner
		}

		// 	assign a wallet_estuary
		if (WalletRequest{} != dealRequest.Wallet) {

			// get wallet from wallets database
			var wallet model.Wallet

			if dealRequest.Wallet.Address != "" {
				node.DB.Where("addr = ? and owner = ?", dealRequest.Wallet.Address, authParts[1]).First(&wallet)
			} else if dealRequest.Wallet.Uuid != "" {
				node.DB.Where("uuid = ? and owner = ?", dealRequest.Wallet.Uuid, authParts[1]).First(&wallet)
			} else {
				node.DB.Where("id = ? and owner = ?", dealRequest.Wallet.Id, authParts[1]).First(&wallet)
			}

			if wallet.ID == 0 {
				return errors.New("Wallet not found, please make sure the wallet is registered")
			}

			// create the wallet request object
			var hexedWallet WalletRequest
			hexedWallet.KeyType = wallet.KeyType
			hexedWallet.PrivateKey = wallet.PrivateKey

			if err != nil {
				return errors.New("Error encoding the wallet")
			}

			// assign the wallet to the content
			contentWalletAssignment := model.ContentWallet{
				WalletId:  wallet.ID,
				Content:   content.ID,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			node.DB.Create(&contentWalletAssignment)

			dealRequest.Wallet = WalletRequest{
				Id:      dealRequest.Wallet.Id,
				Address: wallet.Addr,
			}
		}

		var dealProposalParam model.ContentDealProposalParameters
		dealProposalParam.CreatedAt = time.Now()
		dealProposalParam.UpdatedAt = time.Now()
		dealProposalParam.Content = content.ID
		dealProposalParam.UnverifiedDealMaxPrice = func() string {
			if dealRequest.UnverifiedDealMaxPrice != "" {
				return dealRequest.UnverifiedDealMaxPrice
			}
			return "0"
		}()
		dealProposalParam.Label = func() string {
			if dealRequest.Label != "" {
				return dealRequest.Label
			}
			return content.Cid
		}()
		dealProposalParam.SkipIPNIAnnounce = dealRequest.SkipIPNIAnnounce
		dealProposalParam.VerifiedDeal = func() bool {
			if dealRequest.DealVerifyState == utils.DEAL_UNVERIFIED {
				return false
			}
			return true
		}()

		// start epoch
		if dealRequest.StartEpoch != 0 {
			dealProposalParam.StartEpoch = dealRequest.StartEpoch
		}
		if dealRequest.StartEpochInDays != 0 && dealRequest.DurationInDays != 0 {
			startEpochTime := time.Now().AddDate(0, 0, int(dealRequest.StartEpochInDays))
			dealProposalParam.StartEpoch = utils.DateToHeight(startEpochTime)
			dealProposalParam.EndEpoch = dealProposalParam.StartEpoch + (utils.EPOCH_PER_DAY * (dealRequest.DurationInDays))
			dealProposalParam.Duration = dealProposalParam.EndEpoch - dealProposalParam.StartEpoch
		}
		dealProposalParam.RemoveUnsealedCopy = dealRequest.RemoveUnsealedCopy
		dealProposalParam.SkipIPNIAnnounce = dealRequest.SkipIPNIAnnounce

		// deal proposal parameters
		node.DB.Create(&dealProposalParam)

		if err != nil {
			return errors.New("Error pinning the file")
		}

		var dispatchJobs core.IProcessor
		if pieceCommp.ID != 0 {
			dispatchJobs = jobs.NewStorageDealMakerProcessor(node, content, pieceCommp) // straight to storage deal making
		} else {
			dispatchJobs = jobs.NewPieceCommpProcessor(node, content) // straight to pieceCommp
		}

		node.Dispatcher.AddJobAndDispatch(dispatchJobs, 1)

		err = c.JSON(200, DealResponse{
			Status:                       "success",
			Message:                      "Deal request received. Please take note of the content_id. You can use the content_id to check the status of the deal.",
			ContentId:                    content.ID,
			DealRequest:                  dealRequest,
			DealProposalParameterRequest: dealProposalParam,
		})
		if err != nil {
			return err
		}
		return nil
	})

	if errTxn != nil {
		return errors.New("Error creating the content record" + " " + errTxn.Error())
	}
	return nil
}

// handleEndToEndDeal This function handles the end-to-end deal process for a given
// @Summary End-to-end deal
// @Description This endpoint allows you to make a deal request with a single request
// @Tags deal
// file and metadata in a Go application.
func handleEndToEndDeal(c echo.Context, node *core.DeltaNode) error {
	var dealRequest DealRequest

	authorizationString := c.Request().Header.Get("Authorization")
	authParts := strings.Split(authorizationString, " ")
	file, err := c.FormFile("data") // file
	meta := c.FormValue("metadata")

	//	validate the meta
	err = json.Unmarshal([]byte(meta), &dealRequest)
	if err != nil {
		return err
	}

	if dealRequest.ConnectionMode == "import" {
		return errors.New("Connection mode import is not supported for end-to-end deal endpoint")
	}

	// fail safe
	dealRequest.ConnectionMode = "e2e"

	err = ValidateMeta(dealRequest, node)

	// validate the file if it's more than 1mb
	if file.Size < 1000000 && dealRequest.DealVerifyState == utils.DEAL_VERIFIED {
		return errors.New("File size is too small")
	}

	if err != nil {
		// return the error from the validation
		return err
	}

	// process the file
	src, err := file.Open()
	if err != nil {
		return errors.New("Error opening the file")
	}

	addNode, err := node.Node.AddPinFile(c.Request().Context(), src, nil)
	if err != nil {
		return errors.New("Error pinning the file")
	}

	// wrap in a transaction so we can rollback if something goes wrong
	errTxn := node.DB.Transaction(func(tx *gorm.DB) error {

		// let's create a commp but only if we have
		// a cid, a piece_cid, a padded_piece_size, size
		var pieceCommp model.PieceCommitment
		if (PieceCommitmentRequest{} != dealRequest.PieceCommitment && dealRequest.PieceCommitment.Piece != "") &&
			(dealRequest.PieceCommitment.PaddedPieceSize != 0) &&
			(dealRequest.Size != 0) {

			// if commp is there, make sure the piece and size are there. Use default duration.
			pieceCommp.Cid = addNode.Cid().String()
			pieceCommp.Piece = dealRequest.PieceCommitment.Piece
			pieceCommp.Size = file.Size
			pieceCommp.UnPaddedPieceSize = dealRequest.PieceCommitment.UnPaddedPieceSize
			pieceCommp.PaddedPieceSize = dealRequest.PieceCommitment.PaddedPieceSize
			pieceCommp.CreatedAt = time.Now()
			pieceCommp.UpdatedAt = time.Now()
			pieceCommp.Status = utils.COMMP_STATUS_OPEN
			tx.Create(&pieceCommp)

			dealRequest.PieceCommitment = PieceCommitmentRequest{
				Piece:             pieceCommp.Piece,
				PaddedPieceSize:   pieceCommp.PaddedPieceSize,
				UnPaddedPieceSize: pieceCommp.UnPaddedPieceSize,
			}
		}

		// save the content to the DB with the piece_commitment_id
		content := model.Content{
			Name:              file.Filename,
			Size:              file.Size,
			Cid:               addNode.Cid().String(),
			RequestingApiKey:  authParts[1],
			PieceCommitmentId: pieceCommp.ID,
			Status:            utils.CONTENT_PINNED,
			AutoRetry:         dealRequest.AutoRetry,
			ConnectionMode:    dealRequest.ConnectionMode,
			CreatedAt:         time.Now(),
			UpdatedAt:         time.Now(),
		}
		tx.Create(&content)
		dealRequest.Cid = content.Cid

		//	assign a miner
		if dealRequest.Miner == "" {
			minerAssignService := core.NewMinerAssignmentService(*node)
			provider, errOnPv := minerAssignService.GetSPWithGivenBytes(file.Size)
			if errOnPv != nil {
				return errOnPv
			}
			dealRequest.Miner = provider.Address
		}
		if dealRequest.Miner != "" {
			contentMinerAssignment := model.ContentMiner{
				Miner:     dealRequest.Miner,
				Content:   content.ID,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			tx.Create(&contentMinerAssignment)
			dealRequest.Miner = contentMinerAssignment.Miner
		}

		if (WalletRequest{} != dealRequest.Wallet) {

			// get wallet from wallets database
			var wallet model.Wallet

			if dealRequest.Wallet.Address != "" {
				tx.Where("addr = ? and owner = ?", dealRequest.Wallet.Address, authParts[1]).First(&wallet)
			} else if dealRequest.Wallet.Uuid != "" {
				tx.Where("uuid = ? and owner = ?", dealRequest.Wallet.Uuid, authParts[1]).First(&wallet)
			} else {
				tx.Where("id = ? and owner = ?", dealRequest.Wallet.Id, authParts[1]).First(&wallet)
			}

			if wallet.ID == 0 {
				return errors.New("Wallet not found, please make sure the wallet is registered")
			}

			// create the wallet request object
			var hexedWallet WalletRequest
			hexedWallet.KeyType = wallet.KeyType
			hexedWallet.PrivateKey = wallet.PrivateKey

			if err != nil {
				return errors.New("Error encoding the wallet")
			}

			// assign the wallet to the content
			contentWalletAssignment := model.ContentWallet{
				WalletId:  wallet.ID,
				Content:   content.ID,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			tx.Create(&contentWalletAssignment)

			dealRequest.Wallet = WalletRequest{
				Id:      dealRequest.Wallet.Id,
				Address: wallet.Addr,
			}
		}

		var dealProposalParam model.ContentDealProposalParameters
		dealProposalParam.CreatedAt = time.Now()
		dealProposalParam.UpdatedAt = time.Now()
		dealProposalParam.Content = content.ID
		dealProposalParam.UnverifiedDealMaxPrice = func() string {
			if dealRequest.UnverifiedDealMaxPrice != "" {
				return dealRequest.UnverifiedDealMaxPrice
			}
			return "0"
		}()
		dealProposalParam.Label = func() string {
			if dealRequest.Label != "" {
				return dealRequest.Label
			}
			return content.Cid
		}()
		dealProposalParam.VerifiedDeal = func() bool {
			if dealRequest.DealVerifyState == utils.DEAL_UNVERIFIED {
				return false
			}
			return true
		}()

		if dealRequest.StartEpochInDays != 0 && dealRequest.DurationInDays != 0 {
			startEpochTime := time.Now().AddDate(0, 0, int(dealRequest.StartEpochInDays))
			dealProposalParam.StartEpoch = utils.DateToHeight(startEpochTime)
			dealProposalParam.EndEpoch = dealProposalParam.StartEpoch + (utils.EPOCH_PER_DAY * (dealRequest.DurationInDays - dealRequest.StartEpochInDays))
			dealProposalParam.Duration = dealProposalParam.EndEpoch - dealProposalParam.StartEpoch
		} else {
			dealProposalParam.StartEpoch = 0
			dealProposalParam.Duration = utils.DEFAULT_DURATION
		}

		dealProposalParam.RemoveUnsealedCopy = dealRequest.RemoveUnsealedCopy
		dealProposalParam.SkipIPNIAnnounce = dealRequest.SkipIPNIAnnounce

		dealProposalParam.TransferParams = func() string {
			//authToken, err := httptransport.GenerateAuthToken()
			addrstr := node.Node.Config.AnnounceAddrs[1] + "/p2p/" + node.Node.Host.ID().String()
			announceAddr, err := multiaddr.NewMultiaddr(addrstr)
			if err != nil {
				return ""
			}

			transferParamsUrl := func() string {
				if dealRequest.TransferParameters.URL != "" {
					return dealRequest.TransferParameters.URL
				}
				return "libp2p://" + announceAddr.String()
			}()
			transferParams := TransferParameters{
				URL: transferParamsUrl,
				//Headers: transferParamsHeaders,
			}

			stringTP, err := json.Marshal(transferParams)
			if err != nil {
				return ""
			}
			return string(stringTP)

		}()

		// deal proposal parameters
		tx.Create(&dealProposalParam)
		if dealRequest.Replication == 0 {
			var dispatchJobs core.IProcessor
			if pieceCommp.ID != 0 {
				dispatchJobs = jobs.NewStorageDealMakerProcessor(node, content, pieceCommp) // straight to storage deal making
			} else {
				dispatchJobs = jobs.NewPieceCommpProcessor(node, content) // straight to pieceCommp
			}

			node.Dispatcher.AddJobAndDispatch(dispatchJobs, 1)

			err = c.JSON(200, DealResponse{
				Status:                       "success",
				Message:                      "Deal request received. Please take note of the content_id. You can use the content_id to check the status of the deal.",
				ContentId:                    content.ID,
				DealRequest:                  dealRequest,
				DealProposalParameterRequest: dealProposalParam,
			})

		} else {
			dealReplication := DealReplication{
				Content:                      content,
				ContentDealProposalParameter: dealProposalParam,
				DealRequest:                  dealRequest,
			}

			// TODO: Improve this, this is a hack to make sure the replication is done before the deal is made
			contents := ReplicateContent(node, dealReplication, dealRequest, tx)
			var dispatchJobs core.IProcessor
			for _, contentRep := range contents {
				dispatchJobs = jobs.NewPieceCommpProcessor(node, contentRep.Content) // straight to pieceCommp
				node.Dispatcher.AddJob(dispatchJobs)
			}
			dispatchJobs = jobs.NewPieceCommpProcessor(node, content) // straight to pieceCommp
			node.Dispatcher.AddJob(dispatchJobs)

			node.Dispatcher.Start(len(contents) + 1)
			err = c.JSON(200, DealResponse{
				Status:                       "success",
				Message:                      "Deal request received. Please take note of the content_id. You can use the content_id to check the status of the deal.",
				ContentId:                    content.ID,
				DealRequest:                  dealRequest,
				DealProposalParameterRequest: dealProposalParam,
				ReplicatedContents: func() []DealResponse {
					var dealResponses []DealResponse
					for _, contentRep := range contents {
						dealResponses = append(dealResponses, contentRep.DealResponse)
					}
					return dealResponses
				}(),
			})
		}

		if err != nil {
			return err
		}

		// return transaction
		return nil
	})

	if errTxn != nil {
		return errors.New("Error creating the content record" + " " + errTxn.Error())
	}

	return nil
}

// handleOnlineImportDeal handles the online import deal request
// This function handles an online import deal request by validating the request,
// creating a piece commitment record, assigning a miner, creating a wallet request object,
// creating a content deal proposal, and dispatching a storage deal making job.
func handleOnlineImportDeal(c echo.Context, node *core.DeltaNode) error {
	var dealRequest DealRequest

	// lets record this.
	authorizationString := c.Request().Header.Get("Authorization")
	authParts := strings.Split(authorizationString, " ")
	err := c.Bind(&dealRequest)

	if err != nil {
		return errors.New("Error parsing the request, please check the request body if it complies with the spec")
	}

	if dealRequest.ConnectionMode == "import" {
		return errors.New("Connection mode import is not supported on this import endpoint")
	}

	dealRequest.ConnectionMode = "e2e"
	err = ValidateMeta(dealRequest, node)

	if err != nil {
		return err
	}

	err = ValidatePieceCommitmentMeta(dealRequest.PieceCommitment, node)
	if err != nil {
		return err
	}

	errTxn := node.DB.Transaction(func(tx *gorm.DB) error {
		// let's create a commp but only if we have
		// a cid, a piece_cid, a padded_piece_size, size
		var pieceCommp model.PieceCommitment
		if (PieceCommitmentRequest{} != dealRequest.PieceCommitment && dealRequest.PieceCommitment.Piece != "") &&
			(dealRequest.PieceCommitment.PaddedPieceSize != 0) &&
			(dealRequest.Size != 0) {

			// if commp is there, make sure the piece and size are there. Use default duration.
			pieceCommp.Cid = dealRequest.Cid
			pieceCommp.Piece = dealRequest.PieceCommitment.Piece
			pieceCommp.Size = dealRequest.Size
			pieceCommp.UnPaddedPieceSize = dealRequest.PieceCommitment.UnPaddedPieceSize
			pieceCommp.PaddedPieceSize = dealRequest.PieceCommitment.PaddedPieceSize
			pieceCommp.CreatedAt = time.Now()
			pieceCommp.UpdatedAt = time.Now()
			pieceCommp.Status = utils.COMMP_STATUS_COMITTED
			tx.Create(&pieceCommp)

			dealRequest.PieceCommitment = PieceCommitmentRequest{
				Piece:             pieceCommp.Piece,
				PaddedPieceSize:   pieceCommp.PaddedPieceSize,
				UnPaddedPieceSize: pieceCommp.UnPaddedPieceSize,
			}
		}

		// save the content to the DB with the piece_commitment_id
		content := model.Content{
			Name:              dealRequest.Cid,
			Size:              dealRequest.Size,
			Cid:               dealRequest.Cid,
			RequestingApiKey:  authParts[1],
			PieceCommitmentId: pieceCommp.ID,
			AutoRetry:         dealRequest.AutoRetry,
			Status:            utils.CONTENT_DEAL_MAKING_PROPOSAL,
			ConnectionMode:    dealRequest.ConnectionMode,
			CreatedAt:         time.Now(),
			UpdatedAt:         time.Now(),
		}
		tx.Create(&content)
		dealRequest.Cid = content.Cid

		//	assign a miner
		if dealRequest.Miner == "" {
			minerAssignService := core.NewMinerAssignmentService(*node)
			provider, errOnPv := minerAssignService.GetSPWithGivenBytes(dealRequest.Size)
			if errOnPv != nil {
				return errOnPv
			}
			dealRequest.Miner = provider.Address
		}
		if dealRequest.Miner != "" {
			contentMinerAssignment := model.ContentMiner{
				Miner:     dealRequest.Miner,
				Content:   content.ID,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			tx.Create(&contentMinerAssignment)
			dealRequest.Miner = contentMinerAssignment.Miner
		}

		if (WalletRequest{} != dealRequest.Wallet) {

			// get wallet from wallets database
			var wallet model.Wallet
			if dealRequest.Wallet.Address != "" {
				tx.Where("addr = ? and owner = ?", dealRequest.Wallet.Address, authParts[1]).First(&wallet)
			} else if dealRequest.Wallet.Uuid != "" {
				tx.Where("uuid = ? and owner = ?", dealRequest.Wallet.Uuid, authParts[1]).First(&wallet)
			} else {
				tx.Where("id = ? and owner = ?", dealRequest.Wallet.Id, authParts[1]).First(&wallet)
			}

			if wallet.ID == 0 {
				return errors.New("Wallet not found, please make sure the wallet is registered")
			}

			// create the wallet request object
			var hexedWallet WalletRequest
			hexedWallet.KeyType = wallet.KeyType
			hexedWallet.PrivateKey = wallet.PrivateKey

			if err != nil {
				return errors.New("Error encoding the wallet")
			}

			// assign the wallet to the content
			contentWalletAssignment := model.ContentWallet{
				WalletId:  wallet.ID,
				Content:   content.ID,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			tx.Create(&contentWalletAssignment)

			dealRequest.Wallet = WalletRequest{
				Id:      dealRequest.Wallet.Id,
				Address: wallet.Addr,
			}
		}

		var dealProposalParam model.ContentDealProposalParameters
		dealProposalParam.CreatedAt = time.Now()
		dealProposalParam.UpdatedAt = time.Now()
		dealProposalParam.Content = content.ID
		dealProposalParam.UnverifiedDealMaxPrice = func() string {
			if dealRequest.UnverifiedDealMaxPrice != "" {
				return dealRequest.UnverifiedDealMaxPrice
			}
			return "0"
		}()
		dealProposalParam.Label = func() string {
			if dealRequest.Label != "" {
				return dealRequest.Label
			}
			return content.Cid
		}()

		dealProposalParam.VerifiedDeal = func() bool {
			if dealRequest.DealVerifyState == utils.DEAL_UNVERIFIED {
				return false
			}
			return true
		}()

		dealProposalParam.TransferParams = func() string {
			//authToken, err := httptransport.GenerateAuthToken()
			addrstr := node.Node.Config.AnnounceAddrs[1] + "/p2p/" + node.Node.Host.ID().String()
			announceAddr, err := multiaddr.NewMultiaddr(addrstr)
			if err != nil {
				return ""
			}

			transferParamsUrl := func() string {
				if dealRequest.TransferParameters.URL != "" {
					return dealRequest.TransferParameters.URL
				}
				return "libp2p://" + announceAddr.String()
			}()

			transferParams := TransferParameters{
				URL: transferParamsUrl,
				//Headers: transferParamsHeaders,
			}
			stringTP, err := json.Marshal(transferParams)
			if err != nil {
				return ""
			}
			return string(stringTP)
		}()

		if dealRequest.StartEpochInDays != 0 && dealRequest.DurationInDays != 0 {
			startEpochTime := time.Now().AddDate(0, 0, int(dealRequest.StartEpochInDays))
			dealProposalParam.StartEpoch = utils.DateToHeight(startEpochTime)
			dealProposalParam.EndEpoch = dealProposalParam.StartEpoch + (utils.EPOCH_PER_DAY * (dealRequest.DurationInDays - dealRequest.StartEpochInDays))
			dealProposalParam.Duration = dealProposalParam.EndEpoch - dealProposalParam.StartEpoch
		} else {
			dealProposalParam.StartEpoch = 0
			dealProposalParam.Duration = utils.DEFAULT_DURATION
		}
		dealProposalParam.RemoveUnsealedCopy = dealRequest.RemoveUnsealedCopy
		dealProposalParam.SkipIPNIAnnounce = dealRequest.SkipIPNIAnnounce

		// deal proposal parameters
		tx.Create(&dealProposalParam)

		if err != nil {
			return errors.New("Error parsing the request, please check the request body if it complies with the spec")
		}

		var dispatchJobs core.IProcessor
		if pieceCommp.ID != 0 {
			dispatchJobs = jobs.NewStorageDealMakerProcessor(node, content, pieceCommp) // straight to storage deal making
		}

		node.Dispatcher.AddJobAndDispatch(dispatchJobs, 1)

		err = c.JSON(200, DealResponse{
			Status:                       "success",
			Message:                      "Deal request received. Please take note of the content_id. You can use the content_id to check the status of the deal.",
			ContentId:                    content.ID,
			DealRequest:                  dealRequest,
			DealProposalParameterRequest: dealProposalParam,
		})
		if err != nil {
			return err
		}
		//
		return nil
	})

	if errTxn != nil {
		return errors.New("Error creating the piece-commitment record" + " " + errTxn.Error())
	}
	return nil
}

// handleImportDeal handles the request to add a commp record.
// @Summary Add a commp record
// @Description Add a commp record
// @Tags deal
// @Accept  json
// @Produce  json
func handleImportDeal(c echo.Context, node *core.DeltaNode) error {
	var dealRequest DealRequest

	// lets record this.
	authorizationString := c.Request().Header.Get("Authorization")
	authParts := strings.Split(authorizationString, " ")
	err := c.Bind(&dealRequest)

	if err != nil {
		return errors.New("Error parsing the request, please check the request body if it complies with the spec")
	}

	if dealRequest.ConnectionMode == "e2e" {
		return errors.New("Connection mode e2e is not supported on this import endpoint")
	}

	dealRequest.ConnectionMode = "import"
	err = ValidateMeta(dealRequest, node)

	if err != nil {
		return err
	}

	err = ValidatePieceCommitmentMeta(dealRequest.PieceCommitment, node)
	if err != nil {
		return err
	}

	errTxn := node.DB.Transaction(func(tx *gorm.DB) error {
		// let's create a commp but only if we have
		// a cid, a piece_cid, a padded_piece_size, size
		var pieceCommp model.PieceCommitment
		if (PieceCommitmentRequest{} != dealRequest.PieceCommitment && dealRequest.PieceCommitment.Piece != "") &&
			(dealRequest.PieceCommitment.PaddedPieceSize != 0) &&
			(dealRequest.Size != 0) {

			// if commp is there, make sure the piece and size are there. Use default duration.
			pieceCommp.Cid = dealRequest.Cid
			pieceCommp.Piece = dealRequest.PieceCommitment.Piece
			pieceCommp.Size = dealRequest.Size
			pieceCommp.UnPaddedPieceSize = dealRequest.PieceCommitment.UnPaddedPieceSize
			pieceCommp.PaddedPieceSize = dealRequest.PieceCommitment.PaddedPieceSize
			pieceCommp.CreatedAt = time.Now()
			pieceCommp.UpdatedAt = time.Now()
			pieceCommp.Status = utils.COMMP_STATUS_COMITTED
			tx.Create(&pieceCommp)

			dealRequest.PieceCommitment = PieceCommitmentRequest{
				Piece:             pieceCommp.Piece,
				PaddedPieceSize:   pieceCommp.PaddedPieceSize,
				UnPaddedPieceSize: pieceCommp.UnPaddedPieceSize,
			}
		}

		// save the content to the DB with the piece_commitment_id
		content := model.Content{
			Name:              dealRequest.Cid,
			Size:              dealRequest.Size,
			Cid:               dealRequest.Cid,
			RequestingApiKey:  authParts[1],
			PieceCommitmentId: pieceCommp.ID,
			AutoRetry:         dealRequest.AutoRetry,
			Status:            utils.CONTENT_DEAL_MAKING_PROPOSAL,
			ConnectionMode:    dealRequest.ConnectionMode,
			CreatedAt:         time.Now(),
			UpdatedAt:         time.Now(),
		}
		tx.Create(&content)
		dealRequest.Cid = content.Cid

		//	assign a miner
		if dealRequest.Miner == "" {
			minerAssignService := core.NewMinerAssignmentService(*node)
			provider, errOnPv := minerAssignService.GetSPWithGivenBytes(dealRequest.Size)
			if errOnPv != nil {
				return errOnPv
			}
			dealRequest.Miner = provider.Address
		}
		if dealRequest.Miner != "" {
			contentMinerAssignment := model.ContentMiner{
				Miner:     dealRequest.Miner,
				Content:   content.ID,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			tx.Create(&contentMinerAssignment)
			dealRequest.Miner = contentMinerAssignment.Miner
		}

		if (WalletRequest{} != dealRequest.Wallet) {

			// get wallet from wallets database
			var wallet model.Wallet
			if dealRequest.Wallet.Address != "" {
				tx.Where("addr = ? and owner = ?", dealRequest.Wallet.Address, authParts[1]).First(&wallet)
			} else if dealRequest.Wallet.Uuid != "" {
				tx.Where("uuid = ? and owner = ?", dealRequest.Wallet.Uuid, authParts[1]).First(&wallet)
			} else {
				tx.Where("id = ? and owner = ?", dealRequest.Wallet.Id, authParts[1]).First(&wallet)
			}

			if wallet.ID == 0 {
				return errors.New("Wallet not found, please make sure the wallet is registered")
			}

			// create the wallet request object
			var hexedWallet WalletRequest
			hexedWallet.KeyType = wallet.KeyType
			hexedWallet.PrivateKey = wallet.PrivateKey

			if err != nil {
				return errors.New("Error encoding the wallet")
			}

			// assign the wallet to the content
			contentWalletAssignment := model.ContentWallet{
				WalletId:  wallet.ID,
				Content:   content.ID,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			tx.Create(&contentWalletAssignment)

			dealRequest.Wallet = WalletRequest{
				Id:      dealRequest.Wallet.Id,
				Address: wallet.Addr,
			}
		}

		var dealProposalParam model.ContentDealProposalParameters
		dealProposalParam.CreatedAt = time.Now()
		dealProposalParam.UpdatedAt = time.Now()
		dealProposalParam.Content = content.ID
		dealProposalParam.UnverifiedDealMaxPrice = func() string {
			if dealRequest.UnverifiedDealMaxPrice != "" {
				return dealRequest.UnverifiedDealMaxPrice
			}
			return "0"
		}()
		dealProposalParam.Label = func() string {
			if dealRequest.Label != "" {
				return dealRequest.Label
			}
			return content.Cid
		}()

		dealProposalParam.VerifiedDeal = func() bool {
			if dealRequest.DealVerifyState == utils.DEAL_UNVERIFIED {
				return false
			}
			return true
		}()

		dealProposalParam.TransferParams = func() string {
			//authToken, err := httptransport.GenerateAuthToken()
			//addrstr := node.Node.Config.AnnounceAddrs[1] + "/p2p/" + node.Node.Host.ID().String()
			//announceAddr, err := multiaddr.NewMultiaddr(addrstr)
			if err != nil {
				return ""
			}

			transferParamsUrl := func() string {
				return dealRequest.TransferParameters.URL
			}()

			//transferParamsHeaders := func() map[string]string {
			//	if dealRequest.TransferParameters.Headers != nil {
			//		dataMap := dealRequest.TransferParameters.Headers.(map[string]interface{})
			//		stringMap := make(map[string]string)
			//		for key, value := range dataMap {
			//			stringMap[key] = fmt.Sprintf("%v", value)
			//		}
			//
			//		return stringMap
			//	}
			//	return map[string]string{
			//		"Authorization": httptransport.BasicAuthHeader("", authToken),
			//	}
			//}()

			transferParams := TransferParameters{
				URL: transferParamsUrl,
				//Headers: transferParamsHeaders,
			}
			stringTP, err := json.Marshal(transferParams)
			if err != nil {
				return ""
			}
			return string(stringTP)

		}()

		if dealRequest.StartEpochInDays != 0 && dealRequest.DurationInDays != 0 {
			startEpochTime := time.Now().AddDate(0, 0, int(dealRequest.StartEpochInDays))
			dealProposalParam.StartEpoch = utils.DateToHeight(startEpochTime)
			dealProposalParam.EndEpoch = dealProposalParam.StartEpoch + (utils.EPOCH_PER_DAY * (dealRequest.DurationInDays - dealRequest.StartEpochInDays))
			dealProposalParam.Duration = dealProposalParam.EndEpoch - dealProposalParam.StartEpoch
		} else {
			dealProposalParam.StartEpoch = 0
			dealProposalParam.Duration = utils.DEFAULT_DURATION
		}
		dealProposalParam.RemoveUnsealedCopy = dealRequest.RemoveUnsealedCopy
		dealProposalParam.SkipIPNIAnnounce = dealRequest.SkipIPNIAnnounce

		// deal proposal parameters
		tx.Create(&dealProposalParam)

		if err != nil {
			return errors.New("Error parsing the request, please check the request body if it complies with the spec")
		}

		var dispatchJobs core.IProcessor
		if pieceCommp.ID != 0 {
			dispatchJobs = jobs.NewStorageDealMakerProcessor(node, content, pieceCommp) // straight to storage deal making
		}

		node.Dispatcher.AddJobAndDispatch(dispatchJobs, 1)

		err = c.JSON(200, DealResponse{
			Status:                       "success",
			Message:                      "Deal request received. Please take note of the content_id. You can use the content_id to check the status of the deal.",
			ContentId:                    content.ID,
			DealRequest:                  dealRequest,
			DealProposalParameterRequest: dealProposalParam,
		})
		if err != nil {
			return err
		}
		//
		return nil
	})

	if errTxn != nil {
		return errors.New("Error creating the piece-commitment record" + " " + errTxn.Error())
	}
	return nil
}

// handleMultipleOnlineImportDeals handles multiple online import deals.
// This function handles multiple online import deals by validating the request,
// creating a piece commitment, assigning a miner and wallet, creating a content deal proposal,
// and dispatching a storage deal making job.
func handleMultipleOnlineImportDeals(c echo.Context, node *core.DeltaNode) error {
	var dealRequests []DealRequest

	// lets record this.
	authorizationString := c.Request().Header.Get("Authorization")
	authParts := strings.Split(authorizationString, " ")

	//	validate the meta
	err := c.Bind(&dealRequests)
	if err != nil {
		return errors.New("Error parsing the request, please check the request body if it complies with the spec")
	}

	errTxn := node.DB.Transaction(func(tx *gorm.DB) error {
		var dealResponses []DealResponse
		for _, dealRequest := range dealRequests {
			if dealRequest.ConnectionMode == "import" {
				return errors.New("Connection mode import is not supported on this online endpoint")
			}
			dealRequest.ConnectionMode = "e2e"
			err = ValidateMeta(dealRequest, node)
			if err != nil {
				tx.Rollback()
				return err
			}

			err = ValidatePieceCommitmentMeta(dealRequest.PieceCommitment, node)
			if err != nil {
				tx.Rollback()
				return err
			}

			// let's create a commp but only if we have
			// a cid, a piece_cid, a padded_piece_size, size
			var pieceCommp model.PieceCommitment
			if (PieceCommitmentRequest{} != dealRequest.PieceCommitment && dealRequest.PieceCommitment.Piece != "") &&
				(dealRequest.PieceCommitment.PaddedPieceSize != 0) &&
				(dealRequest.Size != 0) {

				// if commp is there, make sure the piece and size are there. Use default duration.
				pieceCommp.Cid = dealRequest.Cid
				pieceCommp.Piece = dealRequest.PieceCommitment.Piece
				pieceCommp.Size = dealRequest.Size
				pieceCommp.UnPaddedPieceSize = dealRequest.PieceCommitment.UnPaddedPieceSize
				pieceCommp.PaddedPieceSize = dealRequest.PieceCommitment.PaddedPieceSize
				pieceCommp.CreatedAt = time.Now()
				pieceCommp.UpdatedAt = time.Now()
				pieceCommp.Status = utils.COMMP_STATUS_COMITTED
				tx.Create(&pieceCommp)

				dealRequest.PieceCommitment = PieceCommitmentRequest{
					Piece:             pieceCommp.Piece,
					PaddedPieceSize:   pieceCommp.PaddedPieceSize,
					UnPaddedPieceSize: pieceCommp.UnPaddedPieceSize,
				}
			}

			// save the content to the DB with the piece_commitment_id
			content := model.Content{
				Name:              dealRequest.Cid,
				Size:              dealRequest.Size,
				Cid:               dealRequest.Cid,
				RequestingApiKey:  authParts[1],
				PieceCommitmentId: pieceCommp.ID,
				AutoRetry:         dealRequest.AutoRetry,
				Status:            utils.CONTENT_DEAL_MAKING_PROPOSAL,
				ConnectionMode:    dealRequest.ConnectionMode,
				CreatedAt:         time.Now(),
				UpdatedAt:         time.Now(),
			}
			tx.Create(&content)
			dealRequest.Cid = content.Cid

			//	assign a miner
			if dealRequest.Miner == "" {
				minerAssignService := core.NewMinerAssignmentService(*node)
				provider, errOnPv := minerAssignService.GetSPWithGivenBytes(dealRequest.Size)
				if errOnPv != nil {
					return errOnPv
				}
				dealRequest.Miner = provider.Address
			}
			if dealRequest.Miner != "" {
				contentMinerAssignment := model.ContentMiner{
					Miner:     dealRequest.Miner,
					Content:   content.ID,
					CreatedAt: time.Now(),
					UpdatedAt: time.Now(),
				}
				tx.Create(&contentMinerAssignment)
				dealRequest.Miner = contentMinerAssignment.Miner
			}

			// 	assign a wallet_estuary
			if (WalletRequest{} != dealRequest.Wallet) {

				// get wallet from wallets database
				var wallet model.Wallet
				if dealRequest.Wallet.Address != "" {
					tx.Where("addr = ? and owner = ?", dealRequest.Wallet.Address, authParts[1]).First(&wallet)
				} else if dealRequest.Wallet.Uuid != "" {
					tx.Where("uu_id = ? and owner = ?", dealRequest.Wallet.Uuid, authParts[1]).First(&wallet)
				} else {
					tx.Where("id = ? and owner = ?", dealRequest.Wallet.Id, authParts[1]).First(&wallet)
				}

				if wallet.ID == 0 {
					tx.Rollback()
					return errors.New("Wallet not found, please make sure the wallet is registered with the API key " + dealRequest.Wallet.Address)
				}

				// create the wallet request object
				var hexedWallet WalletRequest
				hexedWallet.KeyType = wallet.KeyType
				hexedWallet.PrivateKey = wallet.PrivateKey

				if err != nil {
					tx.Rollback()
					return errors.New("Error encoding the wallet")
				}

				// assign the wallet to the content
				contentWalletAssignment := model.ContentWallet{
					WalletId:  wallet.ID,
					Content:   content.ID,
					CreatedAt: time.Now(),
					UpdatedAt: time.Now(),
				}
				tx.Create(&contentWalletAssignment)

				dealRequest.Wallet = WalletRequest{
					Id:      dealRequest.Wallet.Id,
					Address: wallet.Addr,
				}
			}

			var dealProposalParam model.ContentDealProposalParameters
			dealProposalParam.CreatedAt = time.Now()
			dealProposalParam.UpdatedAt = time.Now()
			dealProposalParam.Content = content.ID
			dealProposalParam.UnverifiedDealMaxPrice = func() string {
				if dealRequest.UnverifiedDealMaxPrice != "" {
					return dealRequest.UnverifiedDealMaxPrice
				}
				return "0"
			}()
			dealProposalParam.Label = func() string {
				if dealRequest.Label != "" {
					return dealRequest.Label
				}
				return content.Cid
			}()

			dealProposalParam.VerifiedDeal = func() bool {
				if dealRequest.DealVerifyState == utils.DEAL_UNVERIFIED {
					return false
				}
				return true
			}()

			dealProposalParam.TransferParams = func() string {
				//authToken, err := httptransport.GenerateAuthToken()
				addrstr := node.Node.Config.AnnounceAddrs[1] + "/p2p/" + node.Node.Host.ID().String()
				announceAddr, err := multiaddr.NewMultiaddr(addrstr)
				if err != nil {
					return ""
				}

				transferParamsUrl := func() string {
					if dealRequest.TransferParameters.URL != "" {
						return dealRequest.TransferParameters.URL
					}
					return "libp2p://" + announceAddr.String()
				}()

				transferParams := TransferParameters{
					URL: transferParamsUrl,
					//Headers: transferParamsHeaders,
				}
				stringTP, err := json.Marshal(transferParams)
				if err != nil {
					return ""
				}
				return string(stringTP)

			}()
			if dealRequest.StartEpochInDays != 0 && dealRequest.DurationInDays != 0 {
				startEpochTime := time.Now().AddDate(0, 0, int(dealRequest.StartEpochInDays))
				dealProposalParam.StartEpoch = utils.DateToHeight(startEpochTime)
				dealProposalParam.EndEpoch = dealProposalParam.StartEpoch + (utils.EPOCH_PER_DAY * (dealRequest.DurationInDays - dealRequest.StartEpochInDays))
				dealProposalParam.Duration = dealProposalParam.EndEpoch - dealProposalParam.StartEpoch
			} else {
				dealProposalParam.StartEpoch = 0
				dealProposalParam.Duration = utils.DEFAULT_DURATION
			}

			dealProposalParam.RemoveUnsealedCopy = dealRequest.RemoveUnsealedCopy
			dealProposalParam.SkipIPNIAnnounce = dealRequest.SkipIPNIAnnounce

			// deal proposal parameters
			tx.Create(&dealProposalParam)

			var dispatchJobs core.IProcessor
			if pieceCommp.ID != 0 {
				dispatchJobs = jobs.NewStorageDealMakerProcessor(node, content, pieceCommp) // straight to storage deal making
			}

			node.Dispatcher.AddJob(dispatchJobs)

			dealResponses = append(dealResponses, DealResponse{
				Status:                       "success",
				Message:                      "Deal request received. Please take note of the content_id. You can use the content_id to check the status of the deal.",
				ContentId:                    content.ID,
				DealRequest:                  dealRequest,
				DealProposalParameterRequest: dealProposalParam,
			})

		}
		go node.Dispatcher.Start(len(dealRequests))
		err = c.JSON(http.StatusOK, dealResponses)
		if err != nil {
			tx.Rollback()
			return errors.New("Error sending the response" + err.Error())
		}
		return nil
	})
	if errTxn != nil {
		return errors.New("Error in making a deal proposal " + errTxn.Error())
	}
	return nil
}

// handleMultipleBatchImportDeals handles multiple deals in a single request
// This function handles the import of multiple deals in batches.
func handleMultipleBatchImportDeals(c echo.Context, node *core.DeltaNode) error {
	var dealRequests []DealRequest

	// lets record this.
	authorizationString := c.Request().Header.Get("Authorization")
	authParts := strings.Split(authorizationString, " ")

	//	validate the meta
	err := c.Bind(&dealRequests)
	if err != nil {
		return errors.New("Error parsing the request, please check the request body if it complies with the spec")
	}

	// create a batch import object
	batchImportUuid := uuid.New().String()
	batchImport := model.BatchImport{
		Uuid:      batchImportUuid,
		Status:    utils.BATCH_IMPORT_STATUS_STARTED,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	err = node.DB.Create(&batchImport).Error
	if err != nil {
		return errors.New("Error creating a batch import object")
	}

	//batchContentIdChan := make(chan int64, len(dealRequests))

	// process the batch import async.

	go func() error {
		tx := node.DB
		//errTxn := node.DB.Transaction(func(tx *gorm.DB) error {
		var dealResponses []DealResponse
		for _, dealRequest := range dealRequests {
			if dealRequest.ConnectionMode == "e2e" {
				return errors.New("Connection mode e2e is not supported on this import endpoint")
			}
			dealRequest.ConnectionMode = "import"
			err = ValidateMeta(dealRequest, node)
			if err != nil {
				fmt.Println("Error validating the meta", err)
				return err
			}

			err = ValidatePieceCommitmentMeta(dealRequest.PieceCommitment, node)
			if err != nil {
				fmt.Println("Error validating the piece commitment meta", err)
				return err
			}

			// let's create a commp but only if we have
			// a cid, a piece_cid, a padded_piece_size, size
			var pieceCommp model.PieceCommitment
			if (PieceCommitmentRequest{} != dealRequest.PieceCommitment && dealRequest.PieceCommitment.Piece != "") &&
				(dealRequest.PieceCommitment.PaddedPieceSize != 0) &&
				(dealRequest.Size != 0) {

				// if commp is there, make sure the piece and size are there. Use default duration.
				pieceCommp.Cid = dealRequest.Cid
				pieceCommp.Piece = dealRequest.PieceCommitment.Piece
				pieceCommp.Size = dealRequest.Size
				pieceCommp.UnPaddedPieceSize = dealRequest.PieceCommitment.UnPaddedPieceSize
				pieceCommp.PaddedPieceSize = dealRequest.PieceCommitment.PaddedPieceSize
				pieceCommp.CreatedAt = time.Now()
				pieceCommp.UpdatedAt = time.Now()
				pieceCommp.Status = utils.COMMP_STATUS_COMITTED
				tx.Create(&pieceCommp)

				dealRequest.PieceCommitment = PieceCommitmentRequest{
					Piece:             pieceCommp.Piece,
					PaddedPieceSize:   pieceCommp.PaddedPieceSize,
					UnPaddedPieceSize: pieceCommp.UnPaddedPieceSize,
				}
			}

			// save the content to the DB with the piece_commitment_id
			content := model.Content{
				Name:              dealRequest.Cid,
				Size:              dealRequest.Size,
				Cid:               dealRequest.Cid,
				RequestingApiKey:  authParts[1],
				PieceCommitmentId: pieceCommp.ID,
				AutoRetry:         dealRequest.AutoRetry,
				Status:            utils.CONTENT_DEAL_MAKING_PROPOSAL,
				ConnectionMode:    dealRequest.ConnectionMode,
				CreatedAt:         time.Now(),
				UpdatedAt:         time.Now(),
			}
			tx.Create(&content)
			dealRequest.Cid = content.Cid

			//batchContentIdChan <- content.ID

			batchContent := model.BatchImportContent{
				BatchImportID: batchImport.ID,
				ContentID:     content.ID,
				CreatedAt:     time.Now(),
				UpdatedAt:     time.Now(),
			}
			tx.Create(&batchContent)

			//	assign a miner
			if dealRequest.Miner == "" {
				minerAssignService := core.NewMinerAssignmentService(*node)
				provider, errOnPv := minerAssignService.GetSPWithGivenBytes(dealRequest.Size)
				if errOnPv != nil {
					return errOnPv
				}
				dealRequest.Miner = provider.Address
			}
			if dealRequest.Miner != "" {
				contentMinerAssignment := model.ContentMiner{
					Miner:     dealRequest.Miner,
					Content:   content.ID,
					CreatedAt: time.Now(),
					UpdatedAt: time.Now(),
				}
				tx.Create(&contentMinerAssignment)
				dealRequest.Miner = contentMinerAssignment.Miner
			}

			// 	assign a wallet_estuary
			if (WalletRequest{} != dealRequest.Wallet) {

				// get wallet from wallets database
				var wallet model.Wallet
				if dealRequest.Wallet.Address != "" {
					tx.Where("addr = ? and owner = ?", dealRequest.Wallet.Address, authParts[1]).First(&wallet)
				} else if dealRequest.Wallet.Uuid != "" {
					tx.Where("uu_id = ? and owner = ?", dealRequest.Wallet.Uuid, authParts[1]).First(&wallet)
				} else {
					tx.Where("id = ? and owner = ?", dealRequest.Wallet.Id, authParts[1]).First(&wallet)
				}

				if wallet.ID == 0 {
					//tx.Rollback()
					return errors.New("Wallet not found, please make sure the wallet is registered with the API key " + dealRequest.Wallet.Address)
				}

				// create the wallet request object
				var hexedWallet WalletRequest
				hexedWallet.KeyType = wallet.KeyType
				hexedWallet.PrivateKey = wallet.PrivateKey

				if err != nil {
					//tx.Rollback()
					return errors.New("Error encoding the wallet")
				}

				// assign the wallet to the content
				contentWalletAssignment := model.ContentWallet{
					WalletId:  wallet.ID,
					Content:   content.ID,
					CreatedAt: time.Now(),
					UpdatedAt: time.Now(),
				}
				tx.Create(&contentWalletAssignment)

				dealRequest.Wallet = WalletRequest{
					Id:      dealRequest.Wallet.Id,
					Address: wallet.Addr,
				}
			}

			var dealProposalParam model.ContentDealProposalParameters
			dealProposalParam.CreatedAt = time.Now()
			dealProposalParam.UpdatedAt = time.Now()
			dealProposalParam.Content = content.ID
			dealProposalParam.UnverifiedDealMaxPrice = func() string {
				if dealRequest.UnverifiedDealMaxPrice != "" {
					return dealRequest.UnverifiedDealMaxPrice
				}
				return "0"
			}()
			dealProposalParam.Label = func() string {
				if dealRequest.Label != "" {
					return dealRequest.Label
				}
				return content.Cid
			}()

			dealProposalParam.VerifiedDeal = func() bool {
				if dealRequest.DealVerifyState == utils.DEAL_UNVERIFIED {
					return false
				}
				return true
			}()
			dealProposalParam.TransferParams = func() string {
				//authToken, err := httptransport.GenerateAuthToken()
				//addrstr := node.Node.Config.AnnounceAddrs[1] + "/p2p/" + node.Node.Host.ID().String()
				//announceAddr, err := multiaddr.NewMultiaddr(addrstr)
				if err != nil {
					return ""
				}

				transferParamsUrl := func() string {
					return dealRequest.TransferParameters.URL
				}()

				transferParams := TransferParameters{
					URL: transferParamsUrl,
					//Headers: transferParamsHeaders,
				}
				stringTP, err := json.Marshal(transferParams)
				if err != nil {
					return ""
				}
				return string(stringTP)

			}()
			if dealRequest.StartEpochInDays != 0 && dealRequest.DurationInDays != 0 {
				startEpochTime := time.Now().AddDate(0, 0, int(dealRequest.StartEpochInDays))
				dealProposalParam.StartEpoch = utils.DateToHeight(startEpochTime)
				dealProposalParam.EndEpoch = dealProposalParam.StartEpoch + (utils.EPOCH_PER_DAY * (dealRequest.DurationInDays - dealRequest.StartEpochInDays))
				dealProposalParam.Duration = dealProposalParam.EndEpoch - dealProposalParam.StartEpoch
			} else {
				dealProposalParam.StartEpoch = 0
				dealProposalParam.Duration = utils.DEFAULT_DURATION
			}

			dealProposalParam.RemoveUnsealedCopy = dealRequest.RemoveUnsealedCopy
			dealProposalParam.SkipIPNIAnnounce = dealRequest.SkipIPNIAnnounce

			// deal proposal parameters
			tx.Create(&dealProposalParam)

			var dispatchJobs core.IProcessor
			if pieceCommp.ID != 0 {
				dispatchJobs = jobs.NewStorageDealMakerProcessor(node, content, pieceCommp) // straight to storage deal making
			}

			node.Dispatcher.AddJob(dispatchJobs)

			dealResponses = append(dealResponses, DealResponse{
				Status:                       "success",
				Message:                      "Deal request received. Please take note of the content_id. You can use the content_id to check the status of the deal.",
				ContentId:                    content.ID,
				DealRequest:                  dealRequest,
				DealProposalParameterRequest: dealProposalParam,
			})

		}
		go node.Dispatcher.Start(len(dealRequests))
		err = c.JSON(http.StatusOK, dealResponses)
		if err != nil {
			//tx.Rollback()
			return errors.New("Error sending the response" + err.Error())
		}

		// update the batch import status
		var batchImportToBeUpdate model.BatchImport
		node.DB.Raw("SELECT * FROM batch_imports WHERE id = ?", batchImport.ID).Scan(&batchImportToBeUpdate)
		batchImportToBeUpdate.Status = utils.BATCH_IMPORT_STATUS_COMPLETED
		batchImportToBeUpdate.UpdatedAt = time.Now()
		node.DB.Save(&batchImportToBeUpdate)

		return nil
	}()

	return c.JSON(http.StatusOK, struct {
		Status        string `json:"status"`
		Message       string `json:"message"`
		BatchImportID int64  `json:"batch_import_id"`
	}{
		Status:        "success",
		Message:       "Batch import request received. Please take note of the batch_import_id. You can use the batch_import_id to check the status of the deal.",
		BatchImportID: batchImport.ID,
	})
}

// handleMultipleImportDeals handles the request to add a commp record.
// @Summary Add a commp record
// @Description Add a commp record
// @Tags CommP
// @Accept  json
// @Produce  json
func handleMultipleImportDeals(c echo.Context, node *core.DeltaNode) error {
	var dealRequests []DealRequest

	// lets record this.
	authorizationString := c.Request().Header.Get("Authorization")
	authParts := strings.Split(authorizationString, " ")

	//	validate the meta
	err := c.Bind(&dealRequests)
	if err != nil {
		return errors.New("Error parsing the request, please check the request body if it complies with the spec")
	}

	errTxn := node.DB.Transaction(func(tx *gorm.DB) error {
		var dealResponses []DealResponse
		for _, dealRequest := range dealRequests {
			if dealRequest.ConnectionMode == "e2e" {
				return errors.New("Connection mode e2e is not supported on this import endpoint")
			}
			dealRequest.ConnectionMode = "import"
			err = ValidateMeta(dealRequest, node)
			if err != nil {
				tx.Rollback()
				return err
			}

			err = ValidatePieceCommitmentMeta(dealRequest.PieceCommitment, node)
			if err != nil {
				tx.Rollback()
				return err
			}

			// let's create a commp but only if we have
			// a cid, a piece_cid, a padded_piece_size, size
			var pieceCommp model.PieceCommitment
			if (PieceCommitmentRequest{} != dealRequest.PieceCommitment && dealRequest.PieceCommitment.Piece != "") &&
				(dealRequest.PieceCommitment.PaddedPieceSize != 0) &&
				(dealRequest.Size != 0) {

				// if commp is there, make sure the piece and size are there. Use default duration.
				pieceCommp.Cid = dealRequest.Cid
				pieceCommp.Piece = dealRequest.PieceCommitment.Piece
				pieceCommp.Size = dealRequest.Size
				pieceCommp.UnPaddedPieceSize = dealRequest.PieceCommitment.UnPaddedPieceSize
				pieceCommp.PaddedPieceSize = dealRequest.PieceCommitment.PaddedPieceSize
				pieceCommp.CreatedAt = time.Now()
				pieceCommp.UpdatedAt = time.Now()
				pieceCommp.Status = utils.COMMP_STATUS_COMITTED
				tx.Create(&pieceCommp)

				dealRequest.PieceCommitment = PieceCommitmentRequest{
					Piece:             pieceCommp.Piece,
					PaddedPieceSize:   pieceCommp.PaddedPieceSize,
					UnPaddedPieceSize: pieceCommp.UnPaddedPieceSize,
				}
			}

			// save the content to the DB with the piece_commitment_id
			content := model.Content{
				Name:              dealRequest.Cid,
				Size:              dealRequest.Size,
				Cid:               dealRequest.Cid,
				RequestingApiKey:  authParts[1],
				PieceCommitmentId: pieceCommp.ID,
				AutoRetry:         dealRequest.AutoRetry,
				Status:            utils.CONTENT_DEAL_MAKING_PROPOSAL,
				ConnectionMode:    dealRequest.ConnectionMode,
				CreatedAt:         time.Now(),
				UpdatedAt:         time.Now(),
			}
			tx.Create(&content)
			dealRequest.Cid = content.Cid

			//	assign a miner
			if dealRequest.Miner == "" {
				minerAssignService := core.NewMinerAssignmentService(*node)
				provider, errOnPv := minerAssignService.GetSPWithGivenBytes(dealRequest.Size)
				if errOnPv != nil {
					return errOnPv
				}
				dealRequest.Miner = provider.Address
			}
			if dealRequest.Miner != "" {
				contentMinerAssignment := model.ContentMiner{
					Miner:     dealRequest.Miner,
					Content:   content.ID,
					CreatedAt: time.Now(),
					UpdatedAt: time.Now(),
				}
				tx.Create(&contentMinerAssignment)
				dealRequest.Miner = contentMinerAssignment.Miner
			}

			// 	assign a wallet_estuary
			if (WalletRequest{} != dealRequest.Wallet) {

				// get wallet from wallets database
				var wallet model.Wallet
				if dealRequest.Wallet.Address != "" {
					tx.Where("addr = ? and owner = ?", dealRequest.Wallet.Address, authParts[1]).First(&wallet)
				} else if dealRequest.Wallet.Uuid != "" {
					tx.Where("uu_id = ? and owner = ?", dealRequest.Wallet.Uuid, authParts[1]).First(&wallet)
				} else {
					tx.Where("id = ? and owner = ?", dealRequest.Wallet.Id, authParts[1]).First(&wallet)
				}

				if wallet.ID == 0 {
					tx.Rollback()
					return errors.New("Wallet not found, please make sure the wallet is registered with the API key " + dealRequest.Wallet.Address)
				}

				// create the wallet request object
				var hexedWallet WalletRequest
				hexedWallet.KeyType = wallet.KeyType
				hexedWallet.PrivateKey = wallet.PrivateKey

				if err != nil {
					tx.Rollback()
					return errors.New("Error encoding the wallet")
				}

				// assign the wallet to the content
				contentWalletAssignment := model.ContentWallet{
					WalletId:  wallet.ID,
					Content:   content.ID,
					CreatedAt: time.Now(),
					UpdatedAt: time.Now(),
				}
				tx.Create(&contentWalletAssignment)

				dealRequest.Wallet = WalletRequest{
					Id:      dealRequest.Wallet.Id,
					Address: wallet.Addr,
				}
			}

			var dealProposalParam model.ContentDealProposalParameters
			dealProposalParam.CreatedAt = time.Now()
			dealProposalParam.UpdatedAt = time.Now()
			dealProposalParam.Content = content.ID
			dealProposalParam.UnverifiedDealMaxPrice = func() string {
				if dealRequest.UnverifiedDealMaxPrice != "" {
					return dealRequest.UnverifiedDealMaxPrice
				}
				return "0"
			}()
			dealProposalParam.Label = func() string {
				if dealRequest.Label != "" {
					return dealRequest.Label
				}
				return content.Cid
			}()

			dealProposalParam.VerifiedDeal = func() bool {
				if dealRequest.DealVerifyState == utils.DEAL_UNVERIFIED {
					return false
				}
				return true
			}()
			dealProposalParam.TransferParams = func() string {
				//authToken, err := httptransport.GenerateAuthToken()
				//addrstr := node.Node.Config.AnnounceAddrs[1] + "/p2p/" + node.Node.Host.ID().String()
				//announceAddr, err := multiaddr.NewMultiaddr(addrstr)
				if err != nil {
					return ""
				}

				transferParamsUrl := func() string {
					return dealRequest.TransferParameters.URL
				}()

				//transferParamsHeaders := func() map[string]string {
				//	if dealRequest.TransferParameters.Headers != nil {
				//		dataMap := dealRequest.TransferParameters.Headers.(map[string]interface{})
				//		stringMap := make(map[string]string)
				//		for key, value := range dataMap {
				//			stringMap[key] = fmt.Sprintf("%v", value)
				//		}
				//
				//		return stringMap
				//	}
				//	return map[string]string{
				//		"Authorization": httptransport.BasicAuthHeader("", authToken),
				//	}
				//}()

				transferParams := TransferParameters{
					URL: transferParamsUrl,
					//Headers: transferParamsHeaders,
				}
				stringTP, err := json.Marshal(transferParams)
				if err != nil {
					return ""
				}
				return string(stringTP)

			}()
			if dealRequest.StartEpochInDays != 0 && dealRequest.DurationInDays != 0 {
				startEpochTime := time.Now().AddDate(0, 0, int(dealRequest.StartEpochInDays))
				dealProposalParam.StartEpoch = utils.DateToHeight(startEpochTime)
				dealProposalParam.EndEpoch = dealProposalParam.StartEpoch + (utils.EPOCH_PER_DAY * (dealRequest.DurationInDays - dealRequest.StartEpochInDays))
				dealProposalParam.Duration = dealProposalParam.EndEpoch - dealProposalParam.StartEpoch
			} else {
				dealProposalParam.StartEpoch = 0
				dealProposalParam.Duration = utils.DEFAULT_DURATION
			}

			dealProposalParam.RemoveUnsealedCopy = dealRequest.RemoveUnsealedCopy
			dealProposalParam.SkipIPNIAnnounce = dealRequest.SkipIPNIAnnounce

			// deal proposal parameters
			tx.Create(&dealProposalParam)

			var dispatchJobs core.IProcessor
			if pieceCommp.ID != 0 {
				dispatchJobs = jobs.NewStorageDealMakerProcessor(node, content, pieceCommp) // straight to storage deal making
			}

			node.Dispatcher.AddJob(dispatchJobs)

			dealResponses = append(dealResponses, DealResponse{
				Status:                       "success",
				Message:                      "Deal request received. Please take note of the content_id. You can use the content_id to check the status of the deal.",
				ContentId:                    content.ID,
				DealRequest:                  dealRequest,
				DealProposalParameterRequest: dealProposalParam,
			})

		}
		go node.Dispatcher.Start(len(dealRequests))
		err = c.JSON(http.StatusOK, dealResponses)
		if err != nil {
			tx.Rollback()
			return errors.New("Error sending the response" + err.Error())
		}
		return nil
	})
	if errTxn != nil {
		return errors.New("Error in making a deal proposal " + errTxn.Error())
	}
	return nil
}

// handleContentStats is a function or method in the Go programming language.
// The above code is not complete and does not provide enough information to determine what it is doing.
// It appears to be a function or method named "handleContentStats" in the Go programming language, but without the rest of the code or
// context, it is impossible to determine its purpose or functionality.
func handleContentStats(c echo.Context, statsService core.StatsService) error {
	contentIdParam := c.Param("contentId")
	contentId, err := strconv.Atoi(contentIdParam)
	if err != nil {
		return errors.New("Error looking up the status of the content" + err.Error())
	}

	status, err := statsService.ContentStatus(core.ContentStatsParam{
		ContentId: int64(contentId),
	})

	if err != nil {
		return errors.New("Error looking up the status of the content" + err.Error())

	}

	return c.JSON(200, status)
}

// handleCommitmentPieceStats is a function or method in the Go programming language.
// This function handles commitment piece statistics in a Go web application using a StatsService.
func handleCommitmentPieceStats(c echo.Context, statsService core.StatsService) error {
	pieceCommitmentIdParam := c.Param("piece-commitmentId")
	pieceCommitmentId, err := strconv.Atoi(pieceCommitmentIdParam)
	if err != nil {
		return errors.New("Error looking up the status of the piece commitment" + err.Error())
	}

	status, err := statsService.PieceCommitmentStatus(core.PieceCommitmentStatsParam{
		PieceCommpId: int64(pieceCommitmentId),
	})

	if err != nil {
		return errors.New("Error looking up the status of the piece commitment" + err.Error())
	}

	return c.JSON(200, status)
}

type ValidateMetaResult struct {
	IsValid bool
	Message string
}

// ValidatePieceCommitmentMeta `ValidateMeta` validates the `DealRequest` struct and returns an error if the request is invalid
// This function validates a piece commitment request against a DeltaNode in Go.
func ValidatePieceCommitmentMeta(pieceCommitmentRequest PieceCommitmentRequest, node *core.DeltaNode) error {
	if (PieceCommitmentRequest{} == pieceCommitmentRequest) {
		return errors.New("invalid piece_commitment request. piece_commitment is required")
	}
	return nil
}

// ValidateMeta `ValidateMeta` validates the `DealRequest` struct and returns an error if the request is invalid
// The function validates metadata for a deal request in Go programming language.
func ValidateMeta(dealRequest DealRequest, node *core.DeltaNode) error {

	if (DealRequest{} == dealRequest) {
		return errors.New("invalid deal request")
	}

	if (DealRequest{} != dealRequest && dealRequest.UnverifiedDealMaxPrice != "") {
		if dealRequest.DealVerifyState != utils.DEAL_UNVERIFIED {
			return errors.New("unverified_deal_max_price is only valid for unverified deals, make sure to pass deal_verify_state as unverified")
		}
	}

	if (DealRequest{} != dealRequest && dealRequest.DealVerifyState == utils.DEAL_UNVERIFIED) {
		if dealRequest.UnverifiedDealMaxPrice == "" {
			return errors.New("unverified_deal_max_price is required for unverified deals")
		}
	}

	// check if dealRequest.UnverifiedDealMaxPrice is a valid number
	if (DealRequest{} != dealRequest && dealRequest.UnverifiedDealMaxPrice != "") {
		_, err := strconv.ParseFloat(dealRequest.UnverifiedDealMaxPrice, 64)
		if err != nil {
			return errors.New("unverified_deal_max_price is not a valid number")
		}
	}

	if (DealRequest{} != dealRequest && dealRequest.Replication > 0 && dealRequest.ConnectionMode == utils.CONNECTION_MODE_IMPORT) {
		return errors.New("replication factor is not supported for import mode")
	}

	if (DealRequest{} != dealRequest && dealRequest.DurationInDays > 0 && dealRequest.StartEpochInDays == 0) {
		return errors.New("start_epoch_in_days is required when duration_in_days is set")
	}

	if (DealRequest{} != dealRequest && dealRequest.Replication > node.Config.Common.MaxReplicationFactor) {
		return errors.New("replication factor can only be up to " + strconv.Itoa(node.Config.Common.MaxReplicationFactor))
	}

	if (DealRequest{} != dealRequest && dealRequest.StartEpochInDays > 0 && dealRequest.DurationInDays == 0) {
		return errors.New("duration_in_days is required when start_epoch_in_days is set")
	}

	if (DealRequest{} != dealRequest && dealRequest.StartEpochInDays > 14) {
		return errors.New("start_epoch_in_days can only be 14 days or less")
	}

	if (DealRequest{} != dealRequest && dealRequest.DurationInDays > 540) {
		return errors.New("duration_in_days can only be 540 days or less")
	}

	if dealRequest.StartEpochInDays > dealRequest.DurationInDays {
		return errors.New("start_epoch_in_days cannot be greater than duration_in_days")
	}

	if (DealRequest{} != dealRequest && dealRequest.Replication > 6) {
		return errors.New("replication count is more than allowed (6)")
	}

	// label length must be less than 100
	if (DealRequest{} != dealRequest && len(dealRequest.Label) > 100) {
		return errors.New("label length must be less than 100")
	}

	if (DealRequest{} != dealRequest && dealRequest.DealVerifyState == utils.DEAL_UNVERIFIED) {
		dealRequest.DealVerifyState = utils.DEAL_UNVERIFIED
	} else {
		dealRequest.DealVerifyState = utils.DEAL_VERIFIED
	}

	// connection mode is required
	if (DealRequest{} != dealRequest) {
		switch dealRequest.ConnectionMode {
		case "":
			dealRequest.ConnectionMode = utils.CONNECTION_MODE_E2E
		case utils.CONNECTION_MODE_E2E:
		case utils.CONNECTION_MODE_IMPORT:
		default:
			return errors.New("connection mode can only be e2e or import")
		}
	}

	if dealRequest.ConnectionMode == utils.CONNECTION_MODE_E2E && dealRequest.TransferParameters.URL != "" {
		return errors.New("transfer_parameters is not supported for e2e mode.")
	}

	// piece commitment is required
	if (PieceCommitmentRequest{} != dealRequest.PieceCommitment && dealRequest.PieceCommitment.Piece == "") {
		return errors.New("piece commitment is invalid, make sure you have the cid, piece_cid, size and padded_piece_size or unpadded_piece_size")
	}

	if (PieceCommitmentRequest{} != dealRequest.PieceCommitment && dealRequest.PieceCommitment.Piece != "") &&
		(dealRequest.PieceCommitment.PaddedPieceSize == 0 && dealRequest.PieceCommitment.UnPaddedPieceSize == 0) &&
		(dealRequest.Size == 0) {
		return errors.New("piece commitment is invalid, make sure you have the cid, piece_cid, size and padded_piece_size or unpadded_piece_size")
	}

	// size is required
	if (PieceCommitmentRequest{} != dealRequest.PieceCommitment && dealRequest.Size == 0) {
		return errors.New("piece commitment is invalid, make sure you have the cid, piece_cid, size and padded_piece_size or unpadded_piece_size")
	}

	if (WalletRequest{} != dealRequest.Wallet && dealRequest.Wallet.Address == "") {
		return errors.New("wallet address is required")
	}

	// catch any panics
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("Recovered in f", r)
		}
	}()
	return nil
}

type ReplicatedContent struct {
	Content      model.Content
	DealRequest  DealRequest
	DealResponse DealResponse
}

// ReplicateContent replicates content from a source to a node and returns a list of replicated content.
// This function replicates content from a source to a node and returns a list of replicated content.
func ReplicateContent(node *core.DeltaNode, contentSource DealReplication, dealRequest DealRequest, txn *gorm.DB) []ReplicatedContent {
	var replicatedContents []ReplicatedContent
	for i := 0; i < dealRequest.Replication; i++ {
		var replicatedContent ReplicatedContent
		var dealResponse DealResponse
		var newContent model.Content
		var newContentDealProposalParameter model.ContentDealProposalParameters
		newContent = contentSource.Content
		newContentDealProposalParameter = contentSource.ContentDealProposalParameter
		newContent.ID = 0

		err := txn.Create(&newContent).Error
		if err != nil {
			fmt.Println(err)
			return nil
		}

		newContentDealProposalParameter.ID = 0
		newContentDealProposalParameter.Content = newContent.ID
		err = txn.Create(&newContentDealProposalParameter).Error
		if err != nil {
			//tx.Rollback()
			fmt.Println(err)
			return nil
		}
		//	assign a miner
		minerAssignService := core.NewMinerAssignmentService(*node)
		provider, errOnPv := minerAssignService.GetSPWithGivenBytes(newContent.Size)
		if errOnPv != nil {
			fmt.Println(errOnPv)
			return nil
		}

		contentMinerAssignment := model.ContentMiner{
			Miner:     provider.Address,
			Content:   newContent.ID,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		err = txn.Create(&contentMinerAssignment).Error
		if err != nil {
			//tx.Rollback()
			fmt.Println(err)
			return nil
		}
		dealRequest.Miner = provider.Address
		dealResponse.DealRequest = dealRequest
		dealResponse.ContentId = newContent.ID
		dealResponse.DealProposalParameterRequest = newContentDealProposalParameter
		dealResponse.Status = utils.CONTENT_PINNED
		dealResponse.Message = "Content replication request successful"

		replicatedContent.Content = newContent
		replicatedContent.DealRequest = dealRequest
		replicatedContent.DealResponse = dealResponse

		replicatedContents = append(replicatedContents, replicatedContent)

	}
	return replicatedContents
}

// handlePrepareContent It takes a request, and returns a response
// This function handles preparing content for a given DeltaNode and updates statistics using a StatsService.
func handlePrepareContent(c echo.Context, node *core.DeltaNode, statsService core.StatsService) {
	// > This function is called when a node receives a `PrepareCommitmentPiece` message
}

// handlePrepareCommitmentPiece It takes a request, and returns a response
// This function handles preparing commitment pieces for a given DeltaNode and updates statistics using a StatsService.
func handlePrepareCommitmentPiece() {
	// > This function is called when the user clicks the "Prepare Commitment Pieces" button. It takes the user's input and
	// prepares the commitment pieces
}

// handlePrepareCommitmentPieces It takes a request, and returns a response
// This function handles preparing commitment pieces for a given DeltaNode and updates statistics using a StatsService.
func handlePrepareCommitmentPieces() {
	// This function handles the announcement of content.
}

// handleAnnounceContent It takes a request, and returns a response
// This function handles the announcement of content.
func handleAnnounceContent() {
	//	> This function is called when the user clicks the "Announce Content" button. It takes the user's input and
}

// handleAnnounceCommitmentPiece It takes a request, and returns a response
// This function handles the announcement of commitment pieces.
func handleAnnounceCommitmentPiece() {}

// handleAnnounceCommitmentPieces It takes a request, and returns a response
// This function handles the announcement of commitment pieces.
func handleAnnounceCommitmentPieces() {}
