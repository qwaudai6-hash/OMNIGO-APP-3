package service

import (
	"context"

	"github.com/omnigo/backend/internal/product/models"
	"github.com/omnigo/backend/internal/product/pb"
)

type ProductInventoryGRPCServer struct {
	pb.UnimplementedProductInventoryServiceServer
	service *ProductService
}

func NewProductInventoryGRPCServer(service *ProductService) *ProductInventoryGRPCServer {
	return &ProductInventoryGRPCServer{
		service: service,
	}
}

func (s *ProductInventoryGRPCServer) ReserveProduct(ctx context.Context, req *pb.ReserveRequest) (*pb.ReserveResponse, error) {
	var items []models.OrderItem
	for _, item := range req.Items {
		// CI-17: reject zero/negative quantities — a negative value would pass
		// the `stock >= qty` guard and INFLATE inventory on release.
		if item.Quantity <= 0 {
			return &pb.ReserveResponse{
				Success: false,
				Message: "invalid quantity: must be > 0",
			}, nil
		}
		items = append(items, models.OrderItem{
			ProductTrackingID: item.ProductTrackingId,
			Quantity:          int(item.Quantity),
		})
	}

	res, err := s.service.ReserveStock(ctx, items)
	if err != nil {
		return &pb.ReserveResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}

	var pbItems []*pb.OrderItem
	for _, item := range res.Items {
		pbItems = append(pbItems, &pb.OrderItem{
			ProductTrackingId: item.ProductTrackingID,
			Quantity:          int32(item.Quantity),
			PriceAtCheckout:   item.PriceAtCheckout,
			StoreTrackingId:   item.StoreTrackingID,
			VendorTrackingId:  item.VendorTrackingID,
		})
	}

	return &pb.ReserveResponse{
		Success:          true,
		Message:          "Stock reserved successfully",
		Items:            pbItems,
		VendorTrackingId: res.VendorTrackID,
		StoreTrackingId:  res.StoreTrackID,
	}, nil
}

func (s *ProductInventoryGRPCServer) ReleaseProduct(ctx context.Context, req *pb.ReleaseRequest) (*pb.ReleaseResponse, error) {
	var items []models.OrderItem
	for _, item := range req.Items {
		items = append(items, models.OrderItem{
			ProductTrackingID: item.ProductTrackingId,
			Quantity:          int(item.Quantity),
		})
	}

	err := s.service.ReleaseStock(ctx, items)
	if err != nil {
		return &pb.ReleaseResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}

	return &pb.ReleaseResponse{
		Success: true,
		Message: "Stock released successfully",
	}, nil
}
