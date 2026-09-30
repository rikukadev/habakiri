class StatusEdit < ApplicationRecord
  # 本体の途中の入れ子クラス(モデルではない)。この end の後の宣言は StatusEdit のもの
  class PreservedMedia < ActiveModelSerializers::Model
    attributes :media_attachment, :description
  end

  belongs_to :post
end
