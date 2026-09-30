module Fasp
  class Subscription < ApplicationRecord
    # 暗黙の名前は名前空間の内側から探す(Provider → Fasp::Provider)
    belongs_to :provider, foreign_key: 'fasp_provider_id'
  end
end
